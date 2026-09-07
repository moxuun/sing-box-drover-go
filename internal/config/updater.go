package config

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	InitialUpdateDelay    = time.Minute
	MinimumUpdateInterval = 15 * time.Minute
	maxDuration           = time.Duration(1<<63 - 1)
)

func ClampUpdateInterval(minutes int32) time.Duration {
	if minutes < 15 {
		minutes = 15
	}
	maxMinutes := int64(maxDuration / time.Minute)
	if int64(minutes) > maxMinutes {
		return maxDuration
	}
	return time.Duration(minutes) * time.Minute
}

type BPFUpdater struct {
	Path       string
	Profile    BPFProfile
	HTTPClient *http.Client
	UserAgent  string
	Logf       func(string, ...any)
}

func (u *BPFUpdater) logf(format string, args ...any) {
	if u.Logf != nil {
		u.Logf(format, args...)
	}
}

func (u *BPFUpdater) update(ctx context.Context) error {
	client := u.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: nil}}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.Profile.RemotePath, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", u.UserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if len(body) >= 32<<20 {
		return fmt.Errorf("remote configuration is too large")
	}
	jsonText := readUTF8(body)
	candidateConfig, err := ReadSingBoxConfig(jsonText)
	if err != nil {
		return err
	}
	if err := CheckSingBoxConfig(candidateConfig); err != nil {
		return fmt.Errorf("remote configuration is unusable: %w", err)
	}
	profile := u.Profile
	profile.ConfigJSON = jsonText
	profile.LastUpdated = time.Now().UnixMilli()
	data, err := EncodeBPF(profile)
	if err != nil {
		return err
	}
	if err := atomicWrite(u.Path, data); err != nil {
		return err
	}
	u.Profile = profile
	return nil
}

func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".sing-box-drover-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return replaceFile(tmpName, path)
}

func (u *BPFUpdater) Run(ctx context.Context) {
	if !u.Profile.IsRemote() || !u.Profile.AutoUpdate || strings.TrimSpace(u.Profile.RemotePath) == "" {
		return
	}
	timer := time.NewTimer(InitialUpdateDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	for {
		if err := u.update(ctx); err != nil {
			u.logf("config update failed: %v", err)
		} else {
			u.logf("config updated successfully at %s", strconv.FormatInt(u.Profile.LastUpdated, 10))
		}
		timer.Reset(ClampUpdateInterval(u.Profile.AutoUpdateInterval))
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}

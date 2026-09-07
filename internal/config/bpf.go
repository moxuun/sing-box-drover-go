package config

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	BPFMessageTypeProfileContent byte = 3
	BPFProfileTypeLocal               = 0
	BPFProfileTypeICloud              = 1
	BPFProfileTypeRemote              = 2
	BPFVersion0                       = 0
	BPFVersion1                       = 1
	BPFCurrentVersion                 = BPFVersion1
	maxBPFDecodedBytes                = 64 << 20
	maxBPFStringBytes                 = 32 << 20
)

var ErrInvalidBPF = errors.New("invalid BPF profile")

// BPFProfile is the profile-content payload used by sing-box clients.
type BPFProfile struct {
	Version            byte
	Name               string
	ProfileType        int32
	ConfigJSON         string
	RemotePath         string
	AutoUpdate         bool
	AutoUpdateInterval int32
	LastUpdated        int64
}

func (p BPFProfile) IsLocal() bool  { return p.ProfileType == BPFProfileTypeLocal }
func (p BPFProfile) IsRemote() bool { return p.ProfileType == BPFProfileTypeRemote }

func LooksLikeBPF(data []byte) bool {
	return len(data) >= 4 && data[0] == BPFMessageTypeProfileContent && data[2] == 0x1f && data[3] == 0x8b
}

type bpfReader struct {
	data []byte
	pos  int
}

func (r *bpfReader) take(n int) ([]byte, error) {
	if n < 0 || n > len(r.data)-r.pos {
		return nil, fmt.Errorf("%w: unexpected end of data", ErrInvalidBPF)
	}
	v := r.data[r.pos : r.pos+n]
	r.pos += n
	return v, nil
}

func (r *bpfReader) byte() (byte, error) {
	v, err := r.take(1)
	if err != nil {
		return 0, err
	}
	return v[0], nil
}

func (r *bpfReader) uvarint() (uint64, error) {
	var value uint64
	for shift := uint(0); shift <= 63; shift += 7 {
		v, err := r.byte()
		if err != nil {
			return 0, err
		}
		if shift == 63 && v > 1 {
			return 0, fmt.Errorf("%w: invalid varint", ErrInvalidBPF)
		}
		value |= uint64(v&0x7f) << shift
		if v&0x80 == 0 {
			return value, nil
		}
	}
	return 0, fmt.Errorf("%w: invalid varint", ErrInvalidBPF)
}

func (r *bpfReader) string() (string, error) {
	n, err := r.uvarint()
	if err != nil {
		return "", err
	}
	if n > maxBPFStringBytes {
		return "", fmt.Errorf("%w: string too large", ErrInvalidBPF)
	}
	v, err := r.take(int(n))
	if err != nil {
		return "", err
	}
	return string(v), nil
}

func (r *bpfReader) int32BE() (int32, error) {
	v, err := r.take(4)
	if err != nil {
		return 0, err
	}
	return int32(binary.BigEndian.Uint32(v)), nil
}

func (r *bpfReader) int64BE() (int64, error) {
	v, err := r.take(8)
	if err != nil {
		return 0, err
	}
	return int64(binary.BigEndian.Uint64(v)), nil
}

func decompressBPF(data []byte) ([]byte, error) {
	if len(data) > maxBPFDecodedBytes {
		return nil, fmt.Errorf("%w: compressed payload too large", ErrInvalidBPF)
	}
	read := func(r io.ReadCloser) ([]byte, error) {
		defer r.Close()
		return io.ReadAll(io.LimitReader(r, maxBPFDecodedBytes+1))
	}
	if r, err := gzip.NewReader(bytes.NewReader(data)); err == nil {
		out, readErr := read(r)
		if readErr != nil {
			return nil, readErr
		}
		if len(out) > maxBPFDecodedBytes {
			return nil, fmt.Errorf("%w: decompressed payload too large", ErrInvalidBPF)
		}
		return out, nil
	}
	r, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: gzip payload: %v", ErrInvalidBPF, err)
	}
	out, readErr := read(r)
	if readErr != nil {
		return nil, readErr
	}
	if len(out) > maxBPFDecodedBytes {
		return nil, fmt.Errorf("%w: decompressed payload too large", ErrInvalidBPF)
	}
	return out, nil
}

func DecodeBPF(data []byte) (BPFProfile, error) {
	var profile BPFProfile
	if !LooksLikeBPF(data) {
		return profile, fmt.Errorf("%w: bad header", ErrInvalidBPF)
	}
	if data[1] > BPFCurrentVersion {
		return profile, fmt.Errorf("%w: unsupported version %d", ErrInvalidBPF, data[1])
	}
	payload, err := decompressBPF(data[2:])
	if err != nil {
		return profile, err
	}
	r := bpfReader{data: payload}
	profile.Version = data[1]
	if profile.Name, err = r.string(); err != nil {
		return BPFProfile{}, err
	}
	if profile.ProfileType, err = r.int32BE(); err != nil {
		return BPFProfile{}, err
	}
	if profile.ConfigJSON, err = r.string(); err != nil {
		return BPFProfile{}, err
	}
	if profile.ProfileType != BPFProfileTypeLocal {
		if profile.RemotePath, err = r.string(); err != nil {
			return BPFProfile{}, err
		}
	}
	if profile.ProfileType == BPFProfileTypeRemote || (profile.Version == BPFVersion0 && profile.ProfileType != BPFProfileTypeLocal) {
		var flag byte
		if flag, err = r.byte(); err != nil {
			return BPFProfile{}, err
		}
		switch flag {
		case 0:
		case 1:
			profile.AutoUpdate = true
		default:
			return BPFProfile{}, fmt.Errorf("%w: invalid boolean", ErrInvalidBPF)
		}
		if profile.Version >= BPFVersion1 {
			if profile.AutoUpdateInterval, err = r.int32BE(); err != nil {
				return BPFProfile{}, err
			}
		}
		if profile.LastUpdated, err = r.int64BE(); err != nil {
			return BPFProfile{}, err
		}
	}
	return profile, nil
}

func TryDecodeBPF(data []byte) (BPFProfile, bool) {
	if !LooksLikeBPF(data) {
		return BPFProfile{}, false
	}
	p, err := DecodeBPF(data)
	return p, err == nil
}

type bpfWriter struct{ bytes.Buffer }

func (w *bpfWriter) uvarint(v uint64) {
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			b |= 0x80
		}
		w.WriteByte(b)
		if v == 0 {
			return
		}
	}
}

func (w *bpfWriter) string(v string) {
	b := []byte(v)
	w.uvarint(uint64(len(b)))
	w.Write(b)
}

func (w *bpfWriter) int32BE(v int32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(v))
	w.Write(b[:])
}

func (w *bpfWriter) int64BE(v int64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(v))
	w.Write(b[:])
}

func EncodeBPF(profile BPFProfile) ([]byte, error) {
	if profile.Version == BPFVersion0 {
		profile.Version = BPFCurrentVersion
	}
	if profile.Version > BPFCurrentVersion {
		return nil, fmt.Errorf("%w: unsupported version %d", ErrInvalidBPF, profile.Version)
	}
	var payload bpfWriter
	payload.string(profile.Name)
	payload.int32BE(profile.ProfileType)
	payload.string(profile.ConfigJSON)
	if profile.ProfileType != BPFProfileTypeLocal {
		payload.string(profile.RemotePath)
	}
	if profile.ProfileType == BPFProfileTypeRemote {
		if profile.AutoUpdate {
			payload.WriteByte(1)
		} else {
			payload.WriteByte(0)
		}
		payload.int32BE(profile.AutoUpdateInterval)
		payload.int64BE(profile.LastUpdated)
	}
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write(payload.Bytes()); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	result := make([]byte, 2+compressed.Len())
	result[0] = BPFMessageTypeProfileContent
	result[1] = profile.Version
	copy(result[2:], compressed.Bytes())
	return result, nil
}

func CreateLocalBPF(configJSON, name string) BPFProfile {
	return BPFProfile{Version: BPFCurrentVersion, Name: name, ProfileType: BPFProfileTypeLocal, ConfigJSON: configJSON}
}

func CreateRemoteBPF(configJSON, name, remotePath string, autoUpdate bool, interval int32, lastUpdated int64) BPFProfile {
	return BPFProfile{Version: BPFCurrentVersion, Name: name, ProfileType: BPFProfileTypeRemote, ConfigJSON: configJSON, RemotePath: remotePath, AutoUpdate: autoUpdate, AutoUpdateInterval: interval, LastUpdated: lastUpdated}
}

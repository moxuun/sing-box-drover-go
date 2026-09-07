package config

import (
	"bytes"
	"compress/gzip"
	"testing"
)

func TestBPFRemoteRoundTrip(t *testing.T) {
	want := CreateRemoteBPF(`{"inbounds":[{"type":"mixed","listen":"127.0.0.1","listen_port":1080}]}`, "demo", "https://example.test/config.json", true, 5, 123456)
	data, err := EncodeBPF(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeBPF(data)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("round trip mismatch: %#v != %#v", got, want)
	}
}

func TestBPFVersionZeroRemotePayload(t *testing.T) {
	// Version 0 did not carry an interval but did carry auto-update and a
	// timestamp for non-local profiles.
	var payload bpfWriter
	payload.string("old")
	payload.int32BE(BPFProfileTypeICloud)
	payload.string("{}")
	payload.string("https://example.test")
	payload.WriteByte(1)
	payload.int64BE(42)
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write(payload.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	data := append([]byte{BPFMessageTypeProfileContent, BPFVersion0}, compressed.Bytes()...)
	got, err := DecodeBPF(data)
	if err != nil {
		t.Fatal(err)
	}
	if !got.AutoUpdate || got.LastUpdated != 42 || got.ProfileType != BPFProfileTypeICloud {
		t.Fatalf("unexpected v0 profile: %#v", got)
	}
}

func TestBPFRejectsMalformedVarint(t *testing.T) {
	var payload bpfWriter
	for i := 0; i < 11; i++ {
		payload.WriteByte(0x80)
	}
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	_, _ = zw.Write(payload.Bytes())
	_ = zw.Close()
	data := append([]byte{BPFMessageTypeProfileContent, BPFVersion1}, compressed.Bytes()...)
	if _, err := DecodeBPF(data); err == nil {
		t.Fatal("expected malformed varint error")
	}
}

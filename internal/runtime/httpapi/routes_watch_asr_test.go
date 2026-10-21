package httpapi

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// buildWAV assembles a RIFF/WAVE buffer, optionally inserting a chunk before
// `data` so the parser is exercised on a realistic (non-fixed-offset) layout.
func buildWAV(extraChunk bool, pcm []byte, declaredDataSize int) []byte {
	var buf bytes.Buffer
	buf.WriteString("RIFF")
	tmp := make([]byte, 4)
	binary.LittleEndian.PutUint32(tmp, uint32(36+len(pcm)))
	buf.Write(tmp)
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	binary.LittleEndian.PutUint32(tmp, 16)
	buf.Write(tmp)
	buf.Write([]byte{1, 0}) // PCM
	buf.Write([]byte{1, 0}) // mono
	binary.LittleEndian.PutUint32(tmp, 16000)
	buf.Write(tmp)
	binary.LittleEndian.PutUint32(tmp, 32000)
	buf.Write(tmp)
	buf.Write([]byte{2, 0})  // block align
	buf.Write([]byte{16, 0}) // bits

	if extraChunk {
		// A LIST chunk that some recorders emit before the samples.
		buf.WriteString("LIST")
		binary.LittleEndian.PutUint32(tmp, 4)
		buf.Write(tmp)
		buf.WriteString("INFO")
	}

	buf.WriteString("data")
	size := declaredDataSize
	if size < 0 {
		size = len(pcm)
	}
	binary.LittleEndian.PutUint32(tmp, uint32(size))
	buf.Write(tmp)
	buf.Write(pcm)
	return buf.Bytes()
}

func TestStripWAVHeaderReturnsPCM(t *testing.T) {
	pcm := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06}
	got, ok := stripWAVHeader(buildWAV(false, pcm, -1))
	if !ok {
		t.Fatal("expected a valid WAV to parse")
	}
	if !bytes.Equal(got, pcm) {
		t.Fatalf("PCM mismatch: got %v want %v", got, pcm)
	}
}

// The chunk walk matters: a fixed 44-byte offset would return the LIST chunk's
// bytes mixed into the samples.
func TestStripWAVHeaderSkipsLeadingChunks(t *testing.T) {
	pcm := []byte{0xAA, 0xBB, 0xCC, 0xDD}
	got, ok := stripWAVHeader(buildWAV(true, pcm, -1))
	if !ok {
		t.Fatal("expected a valid WAV with a leading chunk to parse")
	}
	if !bytes.Equal(got, pcm) {
		t.Fatalf("chunk walk failed: got %v want %v", got, pcm)
	}
}

// Streamed writers often declare 0xFFFFFFFF or a stale length; trusting the
// declared size would truncate or overrun, so the buffer length wins.
func TestStripWAVHeaderTrustsBufferWhenSizeLies(t *testing.T) {
	pcm := []byte{0x10, 0x20, 0x30, 0x40, 0x50}
	got, ok := stripWAVHeader(buildWAV(false, pcm, 999999))
	if !ok {
		t.Fatal("expected parsing to succeed despite a bad declared size")
	}
	if !bytes.Equal(got, pcm) {
		t.Fatalf("expected actual buffer contents: got %v want %v", got, pcm)
	}
}

func TestStripWAVHeaderRejectsNonRIFF(t *testing.T) {
	cases := [][]byte{
		[]byte("this is not audio at all"),
		{},
		[]byte("RIFF"), // too short
		append([]byte("RIFF\x00\x00\x00\x00"), []byte("AVI ")...), // wrong form type
	}
	for i, input := range cases {
		if _, ok := stripWAVHeader(input); ok {
			t.Fatalf("case %d: non-WAV input was accepted", i)
		}
	}
}

func TestToEnginePCMHandlesRawPCMAndWAV(t *testing.T) {
	pcm := []byte{7, 8, 9, 10}

	// A WAV upload is unwrapped.
	got, err := toEnginePCM(buildWAV(false, pcm, -1), "audio/wav")
	if err != nil {
		t.Fatalf("wav: %v", err)
	}
	if !bytes.Equal(got, pcm) {
		t.Fatalf("wav unwrap mismatch: %v", got)
	}

	// Raw PCM passes through untouched.
	got, err = toEnginePCM(pcm, "audio/pcm")
	if err != nil {
		t.Fatalf("raw pcm: %v", err)
	}
	if !bytes.Equal(got, pcm) {
		t.Fatalf("raw pcm altered: %v", got)
	}

	// Parameters on the content type must not defeat the match.
	got, err = toEnginePCM(buildWAV(false, pcm, -1), "audio/wav; codecs=1")
	if err != nil {
		t.Fatalf("wav with params: %v", err)
	}
	if !bytes.Equal(got, pcm) {
		t.Fatalf("wav with params mismatch: %v", got)
	}

	// An octet-stream that happens to be RIFF is unwrapped.
	got, err = toEnginePCM(buildWAV(false, pcm, -1), "application/octet-stream")
	if err != nil {
		t.Fatalf("octet-stream: %v", err)
	}
	if !bytes.Equal(got, pcm) {
		t.Fatalf("octet-stream mismatch: %v", got)
	}
}

// An unsupported container must fail loudly: silently forwarding MP3 bytes as
// PCM would produce garbage transcription rather than an error.
func TestToEnginePCMRejectsUnknownContainer(t *testing.T) {
	if _, err := toEnginePCM([]byte{1, 2, 3}, "audio/mpeg"); err == nil {
		t.Fatal("expected audio/mpeg to be rejected")
	}
}

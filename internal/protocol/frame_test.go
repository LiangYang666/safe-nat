package protocol

import (
	"bytes"
	"encoding/binary"
	"net"
	"sync"
	"testing"
)

func header(typ byte, connID uint32, length int) []byte {
	h := make([]byte, HeaderSize)
	h[0] = typ
	binary.BigEndian.PutUint32(h[1:5], connID)
	binary.BigEndian.PutUint32(h[5:9], uint32(length))
	return h
}

func TestReadFrameRoundTrip(t *testing.T) {
	raw := append(header(TypeData, 7, 4), []byte("ping")...)
	f, err := ReadFrame(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if f.Type != TypeData || f.ConnID != 7 || string(f.Payload) != "ping" {
		t.Fatalf("bad frame: %+v", f)
	}
}

func TestReadFrameEmptyPayload(t *testing.T) {
	f, err := ReadFrame(bytes.NewReader(header(TypeHeartbeat, 0, 0)))
	if err != nil {
		t.Fatal(err)
	}
	if f.Type != TypeHeartbeat || len(f.Payload) != 0 {
		t.Fatalf("bad heartbeat frame: %+v", f)
	}
}

func TestReadFrameTooLarge(t *testing.T) {
	raw := header(TypeData, 1, MaxFrameSize+1)
	if _, err := ReadFrame(bytes.NewReader(raw)); err == nil {
		t.Fatal("expected length guard error")
	}
}

func TestReadFrameTruncated(t *testing.T) {
	raw := append(header(TypeData, 1, 100), make([]byte, 10)...)
	if _, err := ReadFrame(bytes.NewReader(raw)); err == nil {
		t.Fatal("expected io error on truncated payload")
	}
}

// TestConnWriterConcurrent exercises the shared writer from several
// goroutines over an in-memory pipe: frames must not interleave and the
// reader must decode each one intact.
func TestConnWriterConcurrent(t *testing.T) {
	srv, cli := net.Pipe()
	defer srv.Close()
	defer cli.Close()

	cw := NewConnWriter(cli)

	const writers = 8
	const perWriter = 200
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < perWriter; j++ {
				payload := bytes.Repeat([]byte{byte(i)}, j%257)
				if err := cw.Write(TypeData, uint32(i*perWriter+j), payload); err != nil {
					t.Errorf("write: %v", err)
					return
				}
			}
		}(i)
	}

	// Read exactly writers*perWriter frames on the other end.
	got := map[uint32]int{}
	for n := 0; n < writers*perWriter; n++ {
		f, err := ReadFrame(srv)
		if err != nil {
			t.Fatalf("read %d: %v", n, err)
		}
		want := int(f.ConnID % uint32(perWriter))
		if len(f.Payload) != want%257 {
			t.Fatalf("connID %d payload corrupt: got %d want %d", f.ConnID, len(f.Payload), want%257)
		}
		if got[f.ConnID]++; got[f.ConnID] > 1 {
			t.Fatalf("duplicate connID %d", f.ConnID)
		}
	}
	wg.Wait()
}

func TestWriteMsgJSON(t *testing.T) {
	srv, cli := net.Pipe()
	defer srv.Close()
	defer cli.Close()

	cw := NewConnWriter(cli)
	type res struct {
		f   Frame
		err error
	}
	got := make(chan res, 1)
	go func() {
		f, err := ReadFrame(srv)
		got <- res{f, err}
	}()
	if err := cw.WriteMsg(TypeLoginResp, 0, LoginResp{OK: true, Results: []TunnelResult{{Name: "ssh", RemotePort: 40022, OK: true}}}); err != nil {
		t.Fatal(err)
	}
	r := <-got
	if r.err != nil {
		t.Fatal(r.err)
	}
	f := r.f
	if f.Type != TypeLoginResp || f.ConnID != 0 {
		t.Fatalf("bad control frame: %+v", f)
	}
	var resp LoginResp
	if err := DecodeJSON(f, &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || len(resp.Results) != 1 || resp.Results[0].Name != "ssh" {
		t.Fatalf("bad decoded resp: %+v", resp)
	}
}

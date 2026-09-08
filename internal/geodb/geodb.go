// Package geodb resolves IPv4 addresses to region text offline using the
// embedded ip2region xdb database (data from lionsoul2014/ip2region,
// Apache-2.0; the xdb binary format is self-describing enough that the tiny
// reader below avoids a third-party dependency).
//
// xdb layout: 256-byte header, then a 256x256 vector index (8 bytes per
// cell: start/end offsets of a segment-index run), then 14-byte segment
// index records { startIP u32, endIP u32, dataLen u16, dataPtr u32 }.
// All integers are little-endian. Segment data is a UTF-8 string like
// "中国|0|四川省|成都市|电信".
package geodb

import (
	_ "embed"

	"encoding/binary"
	"fmt"
	"net/netip"
	"strings"
)

const (
	headerLen    = 256        // reserved header bytes before the vector index
	vecIndexSize = 8          // bytes per vector-index cell (sPtr, ePtr)
	segIdxSize   = 14         // bytes per segment-index record
	vecRows      = 256
	vecCols      = 256
	regionSep    = "|"
)

//go:embed data/ip2region.xdb
var xdbData []byte

var searcher = newSearcher(xdbData)

type Searcher struct {
	buf []byte
}

func newSearcher(buf []byte) *Searcher {
	min := headerLen + vecRows*vecCols*vecIndexSize
	if len(buf) < min {
		panic(fmt.Sprintf("geodb: ip2region.xdb too small (%d bytes, need >= %d)", len(buf), min))
	}
	return &Searcher{buf: buf}
}

// LookupRaw returns the raw xdb region record ("国家|区域|省|市|ISP") for an
// IPv4 address, or "" when the address is invalid, not IPv4, or unknown.
func LookupRaw(ipStr string) string {
	ip, err := netip.ParseAddr(ipStr)
	if err != nil {
		return ""
	}
	ip = ip.Unmap()
	if !ip.Is4() {
		return ""
	}
	a := ip.As4()
	buf := searcher.buf

	// 1. vector-index cell for this /16.
	idx := headerLen + int(a[0])*vecCols*vecIndexSize + int(a[1])*vecIndexSize
	if idx+vecIndexSize > len(buf) {
		return ""
	}
	sPtr := int(binary.LittleEndian.Uint32(buf[idx:]))
	ePtr := int(binary.LittleEndian.Uint32(buf[idx+4:]))
	if sPtr == 0 || ePtr <= sPtr || ePtr > len(buf) {
		return ""
	}

	// 2. binary search the segment-index run.
	target := binary.BigEndian.Uint32(a[:]) // numeric IP as u32
	lo, hi := 0, (ePtr-sPtr)/segIdxSize-1
	for lo <= hi {
		mid := (lo + hi) >> 1
		p := sPtr + mid*segIdxSize
		if p+segIdxSize > len(buf) {
			return ""
		}
		sip := binary.LittleEndian.Uint32(buf[p:])
		eip := binary.LittleEndian.Uint32(buf[p+4:])
		switch {
		case target < sip:
			hi = mid - 1
		case target > eip:
			lo = mid + 1
		default:
			dataLen := int(binary.LittleEndian.Uint16(buf[p+8:]))
			dataPtr := int(binary.LittleEndian.Uint32(buf[p+10:]))
			if dataPtr < 0 || dataPtr+dataLen > len(buf) {
				return ""
			}
			return string(buf[dataPtr : dataPtr+dataLen])
		}
	}
	return ""
}

// Region returns a human display string in the style of the original
// LiangNat panel, e.g. "中国,四川省,成都市 (电信)". Unknown/missing fields
// are skipped. Empty string means the IP is not in the database.
func Region(ipStr string) string {
	raw := LookupRaw(ipStr)
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, regionSep) // country, area, province, city, isp
	if len(parts) < 5 {
		return raw
	}
	// parts[1] is the legacy "area" field ("0" for most rows).
	var name []string
	if p := parts[0]; p != "" && p != "0" {
		name = append(name, p)
	}
	if p := parts[2]; p != "" && p != "0" {
		name = append(name, p)
	}
	if p := parts[3]; p != "" && p != "0" {
		name = append(name, p)
	}
	disp := strings.Join(name, ",")
	if isp := parts[4]; isp != "" && isp != "0" {
		disp += " (" + isp + ")"
	}
	return disp
}

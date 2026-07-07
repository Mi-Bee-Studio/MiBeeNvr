// SPDX-License-Identifier: MIT
//
// TUTK P2P transport adapted from go2rtc (https://github.com/AlexxIT/go2rtc)
// Copyright (c) go2rtc contributors
// Licensed under the MIT License.

package tutk

import (
	"bytes"
	"encoding/binary"
	"net"
	"time"
)

// NewSession25 creates a new Session25 (TUTK protocol version >= 25).
func NewSession25(conn net.Conn, sid []byte) *Session25 {
	return &Session25{
		Session16: NewSession16(conn, sid),
		rb:        NewReorderBuffer(5),
	}
}

// Session25 extends Session16 with fragmentation/reassembly and reorder buffer.
type Session25 struct {
	*Session16

	rb *ReorderBuffer

	seqSendCmd2 uint16
	seqSendCnt  uint16

	seqRecvPkt0 uint16
	seqRecvPkt1 uint16
	seqRecvCmd2 uint16
}

const cmdHdrSize25 = 28

func (s *Session25) SendIOCtrl(ctrlType uint32, ctrlData []byte) []byte {
	size := msgHhrSize + cmdHdrSize25 + 4 + uint16(len(ctrlData))
	msg := s.Msg(size)

	cmd := msg[msgHhrSize:]
	copy(cmd, "\x00\x70\x0b\x00")
	binary.LittleEndian.PutUint16(cmd[4:], s.seqSendCmd1)
	s.seqSendCmd1++

	// 8  0070      command (second time)
	// 10 0300      seq
	// 12 0100      chunks count
	// 14 0000      chunk seq (starts from 0)
	// 16 5500      size
	// 18 0000      random msg id (always 0)
	// 20 03000000  seq (second time)
	// 24 00000000
	// 28 01010000  ctrlType
	cmd[9] = 0x70
	cmd[12] = 1
	binary.LittleEndian.PutUint16(cmd[16:], size-52)

	binary.LittleEndian.PutUint16(cmd[10:], s.seqSendCmd2)
	binary.LittleEndian.PutUint16(cmd[20:], s.seqSendCmd2)
	s.seqSendCmd2++

	data := cmd[28:]
	binary.LittleEndian.PutUint32(data, ctrlType)
	copy(data[4:], ctrlData)
	return msg
}

func (s *Session25) SendFrameData(frameInfo, frameData []byte) []byte {
	return nil
}

func (s *Session25) SessionRead(chID byte, cmd []byte) (res int) {
	if chID != 0 {
		return s.handleCh1(cmd)
	}

	switch cmd[0] {
	case 0x03, 0x05, 0x07:
		for i := 0; cmd != nil; i++ {
			res = s.handleChunk(cmd, i == 0)
			cmd = s.rb.Pop()
		}
		return

	case 0x00:
		_ = s.SessionWrite(0, s.msgAckCounters())
		s.seqRecvCmd2 = binary.LittleEndian.Uint16(cmd[2:])

		switch cmd[1] {
		case 0x10:
			return msgUnknown0010
		case 0x21:
			return msgClientStartAck2
		case 0x70:
			select {
			case s.rawCmd <- cmd[28:]:
			default:
			}
			return msgCommand
		case 0x71:
			return msgCommandAck
		}

	case 0x09:
		seq := binary.LittleEndian.Uint16(cmd[12:])
		if s.seqSendCmd1 > seq {
			return msgCommandAck
		}
		return msgCounters

	case 0x0a:
		_ = s.SessionWrite(0, s.msgAck0A08(cmd))
		return msgUnknown0a08
	}

	return msgUnknown
}

func (s *Session25) handleChunk(cmd []byte, checkSeq bool) int {
	var cmd2 []byte

	flags := cmd[1]
	if flags&0b1000 == 0 {
		cmd2 = cmd[8:]
	} else {
		cmd2 = cmd[16:]
	}

	seq := binary.LittleEndian.Uint16(cmd2[2:])

	if checkSeq {
		if s.rb.Check(seq) {
			s.rb.Next()
		} else {
			s.rb.Push(seq, cmd)
			return msgMediaReorder
		}
	}

	// Check if this is first chunk for frame.
	// Handle protocol bug "0x20 chunk seq for last chunk" and sometimes
	// "0x20 chunk seq for first chunk if only one chunk".
	if binary.LittleEndian.Uint16(cmd2[6:]) == 0 || binary.LittleEndian.Uint16(cmd2[4:]) == 1 {
		s.waitData = s.waitData[:0]
		s.waitCSeq = seq
	} else if seq != s.waitCSeq {
		return msgMediaLost
	}

	s.waitData = append(s.waitData, cmd2[20:]...)

	if flags&0b0001 == 0 {
		s.waitCSeq++
		return msgMediaChunk
	}

	s.seqRecvPkt1 = seq
	_ = s.SessionWrite(0, s.msgAckCounters())

	n := len(s.waitData) - 32
	packetData := [2][]byte{bytes.Clone(s.waitData[n:]), bytes.Clone(s.waitData[:n])}

	select {
	case s.rawPkt <- packetData:
	default:
		// Queue full - drop oldest to keep latest frame
		select {
		case <-s.rawPkt:
		default:
		}
		select {
		case s.rawPkt <- packetData:
		default:
		}
	}
	return msgMediaFrame
}

func (s *Session25) msgAckCounters() []byte {
	msg := s.Msg(msgHhrSize + cmdHdrSize)

	cmd := msg[msgHhrSize:]
	copy(cmd, "\x09\x00\x0b\x00")

	binary.LittleEndian.PutUint16(cmd[4:], s.seqSendCmd1)
	s.seqSendCmd1++

	// seqRecvPkt0 stores previous value of seqRecvPkt1
	binary.LittleEndian.PutUint16(cmd[8:], s.seqRecvPkt0)
	s.seqRecvPkt0 = s.seqRecvPkt1
	binary.LittleEndian.PutUint16(cmd[10:], s.seqRecvPkt1)
	binary.LittleEndian.PutUint16(cmd[12:], s.seqRecvCmd2)

	binary.LittleEndian.PutUint16(cmd[18:], s.seqSendCnt)
	s.seqSendCnt++
	binary.LittleEndian.PutUint16(cmd[20:], uint16(time.Now().UnixMilli()))
	return msg
}

func (s *Session25) handleCh1(cmd []byte) int {
	switch cid := string(cmd[:2]); cid {
	case "\x00\x00": // client start
		return msgClientStart
	case "\x00\x07": // time sync without data
		_ = s.SessionWrite(1, s.msgAck0007(cmd))
		return msgUnknown0007
	case "\x00\x20": // client start2
		_ = s.SessionWrite(1, s.msgAck0020(cmd))
		return msgClientStart2
	case "\x09\x00":
		return msgUnknown0900
	case "\x0a\x08":
		return msgUnknown0a08
	}
	return msgUnknown
}

func (s *Session25) msgAck0020(msg28 []byte) []byte {
	const cmdDataSize = 36

	msg := s.Msg(msgHhrSize + cmdHdrSize25 + cmdDataSize)

	cmd := msg[msgHhrSize:]
	copy(cmd, "\x00\x21\x0b\x00")
	cmd[16] = cmdDataSize
	copy(cmd[20:], msg28[20:24]) // request id (random)

	data := cmd[cmdHdrSize25:]
	data[5] = 1
	data[7] = 1
	data[8] = 1
	data[12] = 4
	copy(data[16:], "\xfb\x07\x1f\x00")
	data[30] = 3
	data[32] = 1
	return msg
}

func (s *Session25) msgAck0A08(msg28 []byte) []byte {
	msg := s.Msg(msgHhrSize + 20)
	cmd := msg[msgHhrSize:]
	copy(cmd, "\x0b\x00\x0b\x00")
	copy(cmd[8:], msg28[8:10])
	return msg
}

// ReorderBuffer is used for UDP incoming data.
// Because the order of the packets may be mixed up.
type ReorderBuffer struct {
	buf  map[uint16][]byte
	seq  uint16
	size int
}

// NewReorderBuffer creates a new ReorderBuffer with the given max size.
func NewReorderBuffer(size int) *ReorderBuffer {
	return &ReorderBuffer{buf: make(map[uint16][]byte), size: size}
}

// Check returns true if this is the seq we are waiting for.
func (r *ReorderBuffer) Check(seq uint16) (ok bool) {
	return seq == r.seq
}

// Next increments the expected sequence number.
func (r *ReorderBuffer) Next() {
	r.seq++
}

// Available returns how many free slots are in the buffer.
func (r *ReorderBuffer) Available() int {
	return r.size - len(r.buf)
}

// Push adds a new item to the buffer.
func (r *ReorderBuffer) Push(seq uint16, data []byte) {
	r.buf[seq] = bytes.Clone(data)
}

// Pop returns the next item in sequence. If the item is missing and the buffer
// is full, the missing item is skipped (dropped).
func (r *ReorderBuffer) Pop() []byte {
	for {
		if data := r.buf[r.seq]; data != nil {
			delete(r.buf, r.seq)
			r.Next()
			return data
		}
		if r.Available() > 0 {
			return nil
		}
		r.Next() // drop item
	}
}

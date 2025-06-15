package wire

import (
	"bytes"
	"io"
	"testing"
	"time"

	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/qerr"

	"github.com/stretchr/testify/require"
)

func TestFrameTypeParsingReturnsNilWhenNothingToRead(t *testing.T) {
	parser := NewFrameParser(true, true)
	frameType, l, err := parser.ParseType(nil, protocol.Encryption1RTT)
	require.Equal(t, io.EOF, err)
	require.Zero(t, frameType)
	require.Zero(t, l)
}

func TestParseLessCommonFrameReturnsNilWhenNothingToRead(t *testing.T) {
	parser := NewFrameParser(true, true)
	l, f, err := parser.ParseLessCommonFrame(MaxStreamDataFrameType, nil, protocol.Version1)
	require.Equal(t, io.EOF, err)
	require.Zero(t, l)
	require.Zero(t, f)
}

func TestFrameParsingSkipsPaddingFrames(t *testing.T) {
	parser := NewFrameParser(true, true)
	b := []byte{0, 0} // 2 PADDING frames
	b, err := (&PingFrame{}).Append(b, protocol.Version1)
	require.NoError(t, err)

	frameType, l, err := parser.ParseType(b, protocol.Encryption1RTT)
	require.NoError(t, err)
	require.Equal(t, 3, l)
	require.Equal(t, PingFrameType, frameType)

	frame, l, err := parser.ParseLessCommonFrame(frameType, b[1:], protocol.Version1)
	require.NoError(t, err)
	require.Equal(t, 0, l)
	require.IsType(t, &PingFrame{}, frame)
}

func TestFrameParsingHandlesPaddingAtEnd(t *testing.T) {
	parser := NewFrameParser(true, true)
	b := []byte{0, 0, 0}

	frameType, l, err := parser.ParseType(b, protocol.Encryption1RTT)
	require.Equal(t, io.EOF, err)
	require.Equal(t, 3, l)
	require.Equal(t, FrameType(0), frameType)
}

func TestFrameParsingParsesSingleFrame(t *testing.T) {
	parser := NewFrameParser(true, true)
	var b []byte
	for range 10 {
		var err error
		b, err = (&PingFrame{}).Append(b, protocol.Version1)
		require.NoError(t, err)
	}
	frameType, l, err := parser.ParseType(b, protocol.Encryption1RTT)
	require.NoError(t, err)
	require.Equal(t, PingFrameType, frameType)
	require.Equal(t, 1, l)

	frame, l, err := parser.ParseLessCommonFrame(frameType, b, protocol.Version1)
	require.NoError(t, err)
	require.Equal(t, 0, l)
	require.IsType(t, &PingFrame{}, frame)
}

func TestFrameParserACK(t *testing.T) {
	parser := NewFrameParser(true, true)
	f := &AckFrame{AckRanges: []AckRange{{Smallest: 1, Largest: 0x13}}}
	b, err := f.Append(nil, protocol.Version1)
	require.NoError(t, err)
	frameType, l, err := parser.ParseType(b, protocol.Encryption1RTT)
	require.NoError(t, err)
	require.Equal(t, AckFrameType, frameType)
	require.Equal(t, 1, l)

	frame, l, err := parser.ParseAckFrame(frameType, b[l:], protocol.Encryption1RTT, protocol.Version1)
	require.NoError(t, err)
	require.NotNil(t, frame)
	require.Equal(t, protocol.PacketNumber(0x13), frame.LargestAcked())
	require.Equal(t, len(b)-1, l)
}

func TestFrameParserAckDelay(t *testing.T) {
	t.Run("1-RTT", func(t *testing.T) {
		testFrameParserAckDelay(t, protocol.Encryption1RTT)
	})
	t.Run("Handshake", func(t *testing.T) {
		testFrameParserAckDelay(t, protocol.EncryptionHandshake)
	})
}

func testFrameParserAckDelay(t *testing.T, encLevel protocol.EncryptionLevel) {
	parser := NewFrameParser(true, true)
	parser.SetAckDelayExponent(protocol.AckDelayExponent + 2)
	f := &AckFrame{
		AckRanges: []AckRange{{Smallest: 1, Largest: 1}},
		DelayTime: time.Second,
	}
	b, err := f.Append(nil, protocol.Version1)
	require.NoError(t, err)
	frameType, l, err := parser.ParseType(b, encLevel)
	require.NoError(t, err)
	require.Equal(t, AckFrameType, frameType)
	require.Equal(t, 1, l)

	frame, l, err := parser.ParseAckFrame(frameType, b[l:], encLevel, protocol.Version1)
	require.NoError(t, err)
	require.Equal(t, len(b)-1, l)
	if encLevel == protocol.Encryption1RTT {
		require.Equal(t, 4*time.Second, frame.DelayTime)
	} else {
		require.Equal(t, time.Second, frame.DelayTime)
	}
}

func TestFrameParserStreamFrames(t *testing.T) {
	parser := NewFrameParser(true, true)
	f := &StreamFrame{
		StreamID: 0x42,
		Offset:   0x1337,
		Fin:      true,
		Data:     []byte("foobar"),
	}
	b, err := f.Append(nil, protocol.Version1)
	require.NoError(t, err)
	frameType, l, err := parser.ParseType(b, protocol.Encryption1RTT)
	require.NoError(t, err)
	require.Equal(t, FrameType(0xd), frameType)
	require.True(t, frameType.IsStreamFrameType())
	require.Equal(t, 1, l)

	// ParseLessCommonFrame should not handle Stream Frames
	frame, l, err := parser.ParseLessCommonFrame(frameType, b[l:], protocol.Version1)
	require.Equal(t, errUnknownFrameType, err)
	require.Nil(t, frame)
	require.Zero(t, l)
}

func TestFrameParserFrames(t *testing.T) {
	tests := []struct {
		name      string
		frameType FrameType
		frame     Frame
	}{
		{
			name:      "MAX_DATA",
			frameType: MaxDataFrameType,
			frame:     &MaxDataFrame{MaximumData: 0xcafe},
		},
		{
			name:      "MAX_STREAM_DATA",
			frameType: MaxStreamDataFrameType,
			frame:     &MaxStreamDataFrame{StreamID: 0xdeadbeef, MaximumStreamData: 0xdecafbad},
		},
		{
			name:      "RESET_STREAM",
			frameType: ResetStreamFrameType,
			frame: &ResetStreamFrame{
				StreamID:  0xdeadbeef,
				FinalSize: 0xdecafbad1234,
				ErrorCode: 0x1337,
			},
		},
		{
			name:      "STOP_SENDING",
			frameType: StopSendingFrameType,
			frame:     &StopSendingFrame{StreamID: 0x42},
		},
		{
			name:      "CRYPTO",
			frameType: CryptoFrameType,
			frame:     &CryptoFrame{Offset: 0x1337, Data: []byte("lorem ipsum")},
		},
		{
			name:      "NEW_TOKEN",
			frameType: NewTokenFrameType,
			frame:     &NewTokenFrame{Token: []byte("foobar")},
		},
		{
			name:      "MAX_STREAMS",
			frameType: BidiMaxStreamsFrameType,
			frame:     &MaxStreamsFrame{Type: protocol.StreamTypeBidi, MaxStreamNum: 0x1337},
		},
		{
			name:      "DATA_BLOCKED",
			frameType: DataBlockedFrameType,
			frame:     &DataBlockedFrame{MaximumData: 0x1234},
		},
		{
			name:      "STREAM_DATA_BLOCKED",
			frameType: StreamDataBlockedFrameType,
			frame:     &StreamDataBlockedFrame{StreamID: 0xdeadbeef, MaximumStreamData: 0xdead},
		},
		{
			name:      "STREAMS_BLOCKED",
			frameType: BidiStreamBlockedFrameType,
			frame:     &StreamsBlockedFrame{Type: protocol.StreamTypeBidi, StreamLimit: 0x1234567},
		},
		{
			name:      "NEW_CONNECTION_ID",
			frameType: NewConnectionIDFrameType,
			frame: &NewConnectionIDFrame{
				SequenceNumber:      0x1337,
				ConnectionID:        protocol.ParseConnectionID([]byte{0xde, 0xad, 0xbe, 0xef}),
				StatelessResetToken: protocol.StatelessResetToken{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
			},
		},
		{
			name:      "RETIRE_CONNECTION_ID",
			frameType: RetireConnectionIDFrameType,
			frame:     &RetireConnectionIDFrame{SequenceNumber: 0x1337},
		},
		{
			name:      "PATH_CHALLENGE",
			frameType: PathChallengeFrameType,
			frame:     &PathChallengeFrame{Data: [8]byte{1, 2, 3, 4, 5, 6, 7, 8}},
		},
		{
			name:      "PATH_RESPONSE",
			frameType: PathResponseFrameType,
			frame:     &PathResponseFrame{Data: [8]byte{1, 2, 3, 4, 5, 6, 7, 8}},
		},
		{
			name:      "CONNECTION_CLOSE",
			frameType: ConnectionCloseFrameType,
			frame:     &ConnectionCloseFrame{IsApplicationError: false, ReasonPhrase: "foobar"},
		},
		{
			name:      "APPLICATION_CLOSE",
			frameType: ApplicationCloseFrameType,
			frame:     &ConnectionCloseFrame{IsApplicationError: true, ReasonPhrase: "foobar"},
		},
		{
			name:      "HANDSHAKE_DONE",
			frameType: HandshakeDoneFrameType,
			frame:     &HandshakeDoneFrame{},
		},
		{
			name:      "RESET_STREAM_AT",
			frameType: ResetStreamAtFrameType,
			frame:     &ResetStreamFrame{StreamID: 0x1337, ReliableSize: 0x42, FinalSize: 0xdeadbeef},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parser := NewFrameParser(true, true)
			b, err := test.frame.Append(nil, protocol.Version1)
			require.NoError(t, err)

			frameType, l, err := parser.ParseType(b, protocol.Encryption1RTT)
			require.NoError(t, err)
			require.Equal(t, test.frameType, frameType)
			require.Equal(t, 1, l)

			frame, l, err := parser.ParseLessCommonFrame(frameType, b[l:], protocol.Version1)
			require.NoError(t, err)
			require.Equal(t, test.frame, frame)
			require.Equal(t, len(b)-1, l)
		})
	}
}

func TestFrameParserDatagramFrame(t *testing.T) {
	parser := NewFrameParser(true, true)
	f := &DatagramFrame{
		Data: []byte("foobar"),
	}
	b, err := f.Append(nil, protocol.Version1)
	require.NoError(t, err)
	frameType, l, err := parser.ParseType(b, protocol.Encryption1RTT)
	require.NoError(t, err)
	require.Equal(t, DatagramNoLengthFrameType, frameType)
	require.Equal(t, 1, l)

	// ParseLessCommonFrame should not be used to handle Datagram Frames
	frame, lw, err := parser.ParseLessCommonFrame(frameType, b[l:], protocol.Version1)
	require.Equal(t, errUnknownFrameType, err)
	require.Nil(t, frame)
	require.Zero(t, lw)

	// ParseDatagramFrame should be used for this type
	datagramFrame, l, err := parser.ParseDatagramFrame(frameType, b[l:], protocol.Version1)
	require.NoError(t, err)
	require.IsType(t, &DatagramFrame{}, datagramFrame)
	require.Equal(t, f.Data, datagramFrame.Data)
}

func checkFrameUnsupported(t *testing.T, err error, expectedFrameType uint64) {
	t.Helper()
	require.ErrorContains(t, err, errUnknownFrameType.Error())
	var transportErr *qerr.TransportError
	require.ErrorAs(t, err, &transportErr)
	require.Equal(t, qerr.FrameEncodingError, transportErr.ErrorCode)
	require.Equal(t, expectedFrameType, transportErr.FrameType)
	require.Equal(t, "unknown frame type", transportErr.ErrorMessage)
}

func TestFrameParserDatagramUnsupported(t *testing.T) {
	parser := NewFrameParser(false, true)
	f := &DatagramFrame{Data: []byte("foobar")}
	b, err := f.Append(nil, protocol.Version1)
	require.NoError(t, err)
	_, _, err = parser.ParseNext(b, protocol.Encryption1RTT, protocol.Version1)
	checkFrameUnsupported(t, err, 0x30)
}

func TestFrameParserResetStreamAtUnsupported(t *testing.T) {
	parser := NewFrameParser(true, false)
	f := &ResetStreamFrame{StreamID: 0x1337, ReliableSize: 0x42, FinalSize: 0xdeadbeef}
	b, err := f.Append(nil, protocol.Version1)
	require.NoError(t, err)
	_, _, err = parser.ParseNext(b, protocol.Encryption1RTT, protocol.Version1)
	checkFrameUnsupported(t, err, 0x24)
}

func TestFrameParserInvalidFrameType(t *testing.T) {
	parser := NewFrameParser(true, true)
	_, _, err := parser.ParseNext(encodeVarInt(0x42), protocol.Encryption1RTT, protocol.Version1)
	checkFrameUnsupported(t, err, 0x42)
}

func TestFrameParsingErrorsOnInvalidFrames(t *testing.T) {
	parser := NewFrameParser(true, true)
	f := &MaxStreamDataFrame{
		StreamID:          0x1337,
		MaximumStreamData: 0xdeadbeef,
	}
	b, err := f.Append(nil, protocol.Version1)
	require.NoError(t, err)
	_, _, err = parser.ParseNext(b[:len(b)-2], protocol.Encryption1RTT, protocol.Version1)
	require.Error(t, err)
	var transportErr *qerr.TransportError
	require.ErrorAs(t, err, &transportErr)
	require.Equal(t, qerr.FrameEncodingError, transportErr.ErrorCode)
}

func framesToBuffer(tb testing.TB, frames ...Frame) []byte {
	buf := []byte{}
	for _, frame := range frames {
		var err error
		buf, err = frame.Append(buf, protocol.Version1)
		if err != nil {
			tb.Error(err)
		}
	}
	return buf
}

func evaluateFrames(tb testing.TB, parser *FrameParser, buf []byte, frames ...Frame) {
	data := buf
	for j := 0; j < len(frames); j++ {
		expectedFrame := frames[j]

		l, f, err := parser.ParseNext(data, protocol.Encryption1RTT, protocol.Version1)
		if err != nil {
			tb.Error(err)
		}
		data = data[l:]

		switch frame := f.(type) {
		case *StreamFrame:
			streamFrame, ok := expectedFrame.(*StreamFrame)
			if !ok {
				tb.Fatalf("Expected StreamFrame but got %v", expectedFrame)
			}

			if streamFrame.StreamID != frame.StreamID || streamFrame.Offset != frame.Offset {
				tb.Fatalf("STREAM frame does not match: %v vs %v", streamFrame, frame)
			}
			frame.PutBack()
		case *AckFrame:
			ackFrame, ok := expectedFrame.(*AckFrame)
			if !ok {
				tb.Fatalf("Expected AckFrame but got %v", expectedFrame)
			}

			if len(frame.AckRanges) != len(ackFrame.AckRanges) {
				tb.Fatalf("ACK frame does not match, len(AckRanges) not equal: %v vs %v", ackFrame, frame)
			}
			if frame.DelayTime != ackFrame.DelayTime {
				tb.Fatalf("ACK frame does not match: %v vs %v", ackFrame, frame)
			}
			for i, ackRange := range ackFrame.AckRanges {
				if frame.AckRanges[i] != ackRange {
					tb.Fatalf("ACK frame does not match: %v vs %v", ackFrame, frame)
				}
			}
		case *DatagramFrame:
			datagramFrame, ok := expectedFrame.(*DatagramFrame)
			if !ok {
				tb.Fatalf("Expected DatagramFrame but got %v", expectedFrame)
			}

			if datagramFrame.DataLenPresent != frame.DataLenPresent || !bytes.Equal(datagramFrame.Data, frame.Data) {
				tb.Fatalf("DatagramFrame does not match: %v vs %v", datagramFrame, frame)
			}
		case *MaxDataFrame:
			maxDataFrame, ok := expectedFrame.(*MaxDataFrame)
			if !ok {
				tb.Fatalf("Expected MaxDataFrame but got %v", expectedFrame)
			}

			if frame.MaximumData != maxDataFrame.MaximumData {
				tb.Fatalf("MAX_DATA frame does not match: %v vs %v", f, maxDataFrame)
			}
		case *MaxStreamsFrame:
			maxStreamsFrame, ok := expectedFrame.(*MaxStreamsFrame)

			if !ok {
				tb.Fatalf("Expected MaxStreamsFrame but got %v", expectedFrame)
			}

			if frame.MaxStreamNum != maxStreamsFrame.MaxStreamNum {
				tb.Fatalf("MAX_STREAMS frame does not match: %v vs %v", f, maxStreamsFrame)
			}
		case *MaxStreamDataFrame:
			maxStreamDataFrame, ok := expectedFrame.(*MaxStreamDataFrame)
			if !ok {
				tb.Fatalf("Expected MaxStreamDataFrame but got %v", expectedFrame)
			}

			if frame.StreamID != maxStreamDataFrame.StreamID ||
				frame.MaximumStreamData != maxStreamDataFrame.MaximumStreamData {
				tb.Fatalf("MAX_STREAM_DATA frame does not match: %v vs %v", f, maxStreamDataFrame)
			}
		case *CryptoFrame:
			cryptoFrame, ok := expectedFrame.(*CryptoFrame)
			if !ok {
				tb.Fatalf("Expected CryptoFrame but got %v", expectedFrame)
			}

			if frame.Offset != cryptoFrame.Offset || !bytes.Equal(frame.Data, cryptoFrame.Data) {
				tb.Fatalf("CRYPTO frame does not match: %v vs %v", f, cryptoFrame)
			}
		case *ResetStreamFrame:
			resetStreamFrame, ok := expectedFrame.(*ResetStreamFrame)
			if !ok {
				tb.Fatalf("Expected ResetStreamFrame but got %v", expectedFrame)
			}

			if frame.StreamID != resetStreamFrame.StreamID || frame.ErrorCode != resetStreamFrame.ErrorCode ||
				frame.FinalSize != resetStreamFrame.FinalSize {
				tb.Fatalf("RESET_STREAM frame does not match: %v vs %v", frame, resetStreamFrame)
			}
		default:
			tb.Fatalf("Frame type not supported in benchmark or should not occur: %v", frame)
		}
	}
}

func benchmarkFrames(b *testing.B, frames ...Frame) {
	buf := framesToBuffer(b, frames...)

	parser := NewFrameParser(true, true)
	parser.SetAckDelayExponent(3)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		evaluateFrames(b, parser, buf, frames...)
	}
}

func TestBenchmarkStreamFrameAllocations(t *testing.T) {
	frames := make([]Frame, 10)
	for i := 0; i < 10; i++ {
		frames[i] = &StreamFrame{
			StreamID:       protocol.StreamID(1337 + i),
			Offset:         protocol.ByteCount(1e7 + i),
			Data:           make([]byte, 200+i),
			DataLenPresent: true,
		}
	}

	buf := framesToBuffer(t, frames...)

	parser := NewFrameParser(true, true)
	parser.SetAckDelayExponent(3)

	numAllocs := testing.AllocsPerRun(100, func() {
		evaluateFrames(t, parser, buf, frames...)
	})
	require.Equal(t, 0.0, numAllocs)
}

func TestBenchmarkAckFrameAllocations(t *testing.T) {
	frames := make([]Frame, 10)
	for i := 0; i < 10; i++ {
		frames[i] = &AckFrame{
			AckRanges: []AckRange{
				{Smallest: protocol.PacketNumber(5000 + i), Largest: protocol.PacketNumber(5200 + i)},
				{Smallest: protocol.PacketNumber(1 + i), Largest: protocol.PacketNumber(4200 + i)},
			},
			DelayTime: time.Duration(int64(time.Millisecond) * int64(i)),
			ECT0:      uint64(5000 + i),
			ECT1:      uint64(i),
			ECNCE:     uint64(10 + i),
		}
	}

	buf := framesToBuffer(t, frames...)

	parser := NewFrameParser(true, true)
	parser.SetAckDelayExponent(3)

	numAllocs := testing.AllocsPerRun(100, func() {
		evaluateFrames(t, parser, buf, frames...)
	})
	require.Equal(t, 0.0, numAllocs)
}

// STREAM and ACK are the most relevant frames for high-throughput transfers.
func BenchmarkParseStreamAndACK(b *testing.B) {
	frames := []Frame{
		&AckFrame{
			AckRanges: []AckRange{
				{Smallest: 5000, Largest: 5200},
				{Smallest: 1, Largest: 4200},
			},
			DelayTime: 42 * time.Millisecond,
			ECT0:      5000,
			ECT1:      0,
			ECNCE:     10,
		},
		&StreamFrame{
			StreamID:       1337,
			Offset:         1e7,
			Data:           make([]byte, 200),
			DataLenPresent: true,
		},
	}
	benchmarkFrames(b, frames...)
}

func BenchmarkParseOtherFrames(b *testing.B) {
	frames := []Frame{
		&MaxDataFrame{MaximumData: 123456},
		&MaxStreamsFrame{MaxStreamNum: 10},
		&MaxStreamDataFrame{StreamID: 1337, MaximumStreamData: 1e6},
		&CryptoFrame{Offset: 1000, Data: make([]byte, 128)},
		&PingFrame{},
		&ResetStreamFrame{StreamID: 87654, ErrorCode: 1234, FinalSize: 1e8},
	}
	benchmarkFrames(b, frames...)
}

func BenchmarkParseAckFrame(b *testing.B) {
	frames := make([]Frame, 10)
	for i := 0; i < 10; i++ {
		frames[i] = &AckFrame{
			AckRanges: []AckRange{
				{Smallest: protocol.PacketNumber(5000 + i), Largest: protocol.PacketNumber(5200 + i)},
				{Smallest: protocol.PacketNumber(1 + i), Largest: protocol.PacketNumber(4200 + i)},
			},
			DelayTime: time.Duration(int64(time.Millisecond) * int64(i)),
			ECT0:      uint64(5000 + i),
			ECT1:      uint64(i),
			ECNCE:     uint64(10 + i),
		}
	}
	benchmarkFrames(b, frames...)
}

func BenchmarkParseStreamFrame(b *testing.B) {
	frames := make([]Frame, 10)
	for i := 0; i < 10; i++ {
		frames[i] = &StreamFrame{
			StreamID:       protocol.StreamID(1337 + i),
			Offset:         protocol.ByteCount(1e7 + i),
			Data:           make([]byte, 200+i),
			DataLenPresent: true,
		}
	}
	benchmarkFrames(b, frames...)
}

func BenchmarkParseDatagramFrame(b *testing.B) {
	frames := make([]Frame, 10)
	for i := 0; i < 10; i++ {
		frames[i] = &DatagramFrame{
			Data:           make([]byte, 200),
			DataLenPresent: true,
		}
	}
	benchmarkFrames(b, frames...)
}

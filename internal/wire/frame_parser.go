package wire

import (
	"errors"
	"fmt"
	"io"
	"reflect"

	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/qerr"
	"github.com/quic-go/quic-go/quicvarint"
)

var errUnknownFrameType = errors.New("unknown frame type")

// The FrameParser parses QUIC frames, one by one.
type FrameParser struct {
	ackDelayExponent      uint8
	supportsDatagrams     bool
	supportsResetStreamAt bool

	// To avoid allocating when parsing, keep a single ACK frame struct.
	// It is used over and over again.
	ackFrame *AckFrame
}

// NewFrameParser creates a new frame parser.
func NewFrameParser(supportsDatagrams, supportsResetStreamAt bool) *FrameParser {
	return &FrameParser{
		supportsDatagrams:     supportsDatagrams,
		supportsResetStreamAt: supportsResetStreamAt,
		ackFrame:              &AckFrame{},
	}
}

func (p *FrameParser) ParseType(b []byte, encLevel protocol.EncryptionLevel) (FrameType, int, error) {
	var parsed int
	for len(b) != 0 {
		typ, l, err := quicvarint.Parse(b)
		parsed += l
		if err != nil {
			return 0, parsed, &qerr.TransportError{
				ErrorCode:    qerr.FrameEncodingError,
				ErrorMessage: err.Error(),
			}
		}
		b = b[l:]
		if typ == 0x0 { // skip PADDING frames
			continue
		}

		frameType, ok := NewFrameType(typ)
		if !ok {
			return 0, parsed, &qerr.TransportError{
				ErrorCode:    qerr.FrameEncodingError,
				ErrorMessage: fmt.Sprintf("unknown frame type: %d", typ),
			}
		}

		if !frameType.isAllowedAtEncLevel(encLevel) {
			return 0, parsed, &qerr.TransportError{
				ErrorCode:    qerr.FrameEncodingError,
				ErrorMessage: fmt.Sprintf("%d not allowed at encryption level %s", frameType, encLevel),
			}
		}

		return FrameType(typ), parsed, nil
	}
	return 0, parsed, io.EOF
}

// ParseLessCommonFrame parses everything except StreamFrame, AckFrame or DatagramFrame.
// These cases should be handled separately for performance reasons.
func (p *FrameParser) ParseLessCommonFrame(frameType FrameType, data []byte, v protocol.Version) (Frame, int, error) {
	var frame Frame
	var l int
	var err error
	//nolint:exhaustive // Common frames should already be handled.
	switch frameType {
	case PingFrameType:
		frame = &PingFrame{}
		l = 0
	case ResetStreamFrameType:
		frame, l, err = parseResetStreamFrame(data, false, v)
	case StopSendingFrameType:
		frame, l, err = parseStopSendingFrame(data, v)
	case CryptoFrameType:
		frame, l, err = parseCryptoFrame(data, v)
	case NewTokenFrameType:
		frame, l, err = parseNewTokenFrame(data, v)
	case MaxDataFrameType:
		frame, l, err = parseMaxDataFrame(data, v)
	case MaxStreamDataFrameType:
		frame, l, err = parseMaxStreamDataFrame(data, v)
	case BidiMaxStreamsFrameType, UniMaxStreamsFrameType:
		frame, l, err = parseMaxStreamsFrame(data, frameType, v)
	case DataBlockedFrameType:
		frame, l, err = parseDataBlockedFrame(data, v)
	case StreamDataBlockedFrameType:
		frame, l, err = parseStreamDataBlockedFrame(data, v)
	case BidiStreamBlockedFrameType, UniStreamBlockedFrameType:
		frame, l, err = parseStreamsBlockedFrame(data, frameType, v)
	case NewConnectionIDFrameType:
		frame, l, err = parseNewConnectionIDFrame(data, v)
	case RetireConnectionIDFrameType:
		frame, l, err = parseRetireConnectionIDFrame(data, v)
	case PathChallengeFrameType:
		frame, l, err = parsePathChallengeFrame(data, v)
	case PathResponseFrameType:
		frame, l, err = parsePathResponseFrame(data, v)
	case ConnectionCloseFrameType, ApplicationCloseFrameType:
		frame, l, err = parseConnectionCloseFrame(data, frameType, v)
	case HandshakeDoneFrameType:
		frame = &HandshakeDoneFrame{}
		l = 0
	case ResetStreamAtFrameType:
		if !p.supportsResetStreamAt {
			err = errUnknownFrameType
		} else {
			frame, l, err = parseResetStreamFrame(data, true, v)
		}
	default:
		err = errUnknownFrameType
	}
	return frame, l, err
}

// ParseNext parses the next frame.
// It skips PADDING frames.
func (p *FrameParser) ParseNext(data []byte, encLevel protocol.EncryptionLevel, v protocol.Version) (int, Frame, error) {
	frame, l, err := p.parseNext(data, encLevel, v)
	return l, frame, err
}

func (p *FrameParser) parseNext(b []byte, encLevel protocol.EncryptionLevel, v protocol.Version) (Frame, int, error) {
	var parsed int
	for len(b) != 0 {
		typ, l, err := quicvarint.Parse(b)
		parsed += l
		if err != nil {
			return nil, parsed, &qerr.TransportError{
				ErrorCode:    qerr.FrameEncodingError,
				ErrorMessage: err.Error(),
			}
		}
		b = b[l:]
		if typ == 0x0 { // skip PADDING frames
			continue
		}

		f, l, err := p.ParseFrame(b, FrameType(typ), encLevel, v)
		parsed += l
		if err != nil {
			return nil, parsed, &qerr.TransportError{
				FrameType:    typ,
				ErrorCode:    qerr.FrameEncodingError,
				ErrorMessage: err.Error(),
			}
		}
		return f, parsed, nil
	}
	return nil, parsed, nil
}

// TODO: Remove function, got replaced by ParseLessCommonFrame
func (p *FrameParser) ParseFrame(b []byte, frameTyp FrameType, encLevel protocol.EncryptionLevel, v protocol.Version) (Frame, int, error) {
	var frame Frame
	var err error
	var l int
	if byte(frameTyp)&0xf8 == 0x8 {
		frame, l, err = ParseStreamFrame(b, frameTyp, v)
	} else {
		switch frameTyp {
		case PingFrameType:
			frame = &PingFrame{}
		case AckFrameType, AckECNFrameType:
			ackDelayExponent := p.ackDelayExponent
			if encLevel != protocol.Encryption1RTT {
				ackDelayExponent = protocol.DefaultAckDelayExponent
			}
			p.ackFrame.Reset()
			l, err = ParseAckFrame(p.ackFrame, b, frameTyp, ackDelayExponent, v)
			frame = p.ackFrame
		case ResetStreamFrameType:
			frame, l, err = parseResetStreamFrame(b, false, v)
		case StopSendingFrameType:
			frame, l, err = parseStopSendingFrame(b, v)
		case CryptoFrameType:
			frame, l, err = parseCryptoFrame(b, v)
		case NewTokenFrameType:
			frame, l, err = parseNewTokenFrame(b, v)
		case MaxDataFrameType:
			frame, l, err = parseMaxDataFrame(b, v)
		case MaxStreamDataFrameType:
			frame, l, err = parseMaxStreamDataFrame(b, v)
		case BidiMaxStreamsFrameType, UniMaxStreamsFrameType:
			frame, l, err = parseMaxStreamsFrame(b, frameTyp, v)
		case DataBlockedFrameType:
			frame, l, err = parseDataBlockedFrame(b, v)
		case StreamDataBlockedFrameType:
			frame, l, err = parseStreamDataBlockedFrame(b, v)
		case BidiStreamBlockedFrameType, UniStreamBlockedFrameType:
			frame, l, err = parseStreamsBlockedFrame(b, frameTyp, v)
		case NewConnectionIDFrameType:
			frame, l, err = parseNewConnectionIDFrame(b, v)
		case RetireConnectionIDFrameType:
			frame, l, err = parseRetireConnectionIDFrame(b, v)
		case PathChallengeFrameType:
			frame, l, err = parsePathChallengeFrame(b, v)
		case PathResponseFrameType:
			frame, l, err = parsePathResponseFrame(b, v)
		case ConnectionCloseFrameType, ApplicationCloseFrameType:
			frame, l, err = parseConnectionCloseFrame(b, frameTyp, v)
		case HandshakeDoneFrameType:
			frame = &HandshakeDoneFrame{}
		case DatagramNoLengthFrameType, DatagramWithLengthFrameType:
			if !p.supportsDatagrams {
				return nil, 0, errUnknownFrameType
			}
			frame, l, err = ParseDatagramFrame(b, frameTyp, v)
		case ResetStreamAtFrameType:
			if !p.supportsResetStreamAt {
				return nil, 0, errUnknownFrameType
			}
			frame, l, err = parseResetStreamFrame(b, true, v)
		default:
			err = errUnknownFrameType
		}
	}
	if err != nil {
		return nil, 0, err
	}
	if !frameTyp.isAllowedAtEncLevel(encLevel) {
		return nil, l, fmt.Errorf("%s not allowed at encryption level %s", reflect.TypeOf(frame).Elem().Name(), encLevel)
	}
	return frame, l, nil
}

func (p *FrameParser) ParseAckFrame(frameType FrameType, data []byte, encLevel protocol.EncryptionLevel, v protocol.Version) (*AckFrame, int, error) {
	ackDelayExponent := p.ackDelayExponent
	if encLevel != protocol.Encryption1RTT {
		ackDelayExponent = protocol.DefaultAckDelayExponent
	}
	p.ackFrame.Reset()
	l, err := ParseAckFrame(p.ackFrame, data, frameType, ackDelayExponent, v)
	ackFrame := p.ackFrame

	return ackFrame, l, err
}

func (p *FrameParser) ParseDatagramFrame(frameType FrameType, data []byte, v protocol.Version) (*DatagramFrame, int, error) {
	if !p.supportsDatagrams {
		err := errUnknownFrameType
		if err != nil {
			return nil, 0, err
		}
	}
	return ParseDatagramFrame(data, frameType, v)
}

// SetAckDelayExponent sets the acknowledgment delay exponent (sent in the transport parameters).
// This value is used to scale the ACK Delay field in the ACK frame.
func (p *FrameParser) SetAckDelayExponent(exp uint8) {
	p.ackDelayExponent = exp
}

func replaceUnexpectedEOF(e error) error {
	if e == io.ErrUnexpectedEOF {
		return io.EOF
	}
	return e
}

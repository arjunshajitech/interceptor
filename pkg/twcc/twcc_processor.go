package twcc

import (
	"github.com/pion/interceptor"
	"github.com/pion/logging"
	"github.com/pion/rtp"
	"math/rand"
	"time"
)

type CustomTWCCProcessor interface {
	Process(int, []byte, []interceptor.RTPHeaderExtension) (int, error)
	TWCCProcessor() *SenderInterceptor
}

type NoopTWCCProcessor struct{}

func (t NoopTWCCProcessor) TWCCProcessor() *SenderInterceptor {
	return nil
}

func (t NoopTWCCProcessor) Process(size int, _ []byte, _ []interceptor.RTPHeaderExtension) (int, error) {
	return size, nil
}

func (t *SenderInterceptor) TWCCProcessor() *SenderInterceptor {
	return t
}

func (t *SenderInterceptor) Process(i int, buf []byte, ext []interceptor.RTPHeaderExtension) (int, error) {
	var hdrExtID uint8
	for _, e := range ext {
		if e.URI == transportCCURI {
			hdrExtID = uint8(e.ID) //nolint:gosec // G115

			break
		}
	}
	if hdrExtID == 0 { // Don't try to read header extension if ID is 0, because 0 is an invalid extension ID
		return 0, nil
	}

	attr := make(interceptor.Attributes)

	header, err := attr.GetRTPHeader(buf[:i])
	if err != nil {
		return 0, err
	}
	var tccExt rtp.TransportCCExtension
	if ext := header.GetExtension(hdrExtID); ext != nil {
		err = tccExt.Unmarshal(ext)
		if err != nil {
			return 0, err
		}

		p := packet{
			hdr:            header,
			sequenceNumber: tccExt.TransportSequence,
			arrivalTime:    time.Since(t.startTime).Microseconds(),
			ssrc:           t.twccSSRC,
		}
		select {
		case <-t.close:
			return 0, errClosed
		case t.packetChan <- p:
		default:
		}
	}

	return i, nil
}

func NewCustomTWCCProcessor() *SenderInterceptor {
	return &SenderInterceptor{
		log:        logging.NewDefaultLoggerFactory().NewLogger("twcc_processor_interceptor"),
		packetChan: make(chan packet),
		close:      make(chan struct{}),
		interval:   100 * time.Millisecond,
		startTime:  time.Now(),
		twccSSRC:   rand.Uint32(),
	}
}

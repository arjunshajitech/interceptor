package twcc

import (
	"fmt"
	"github.com/pion/interceptor"
	"github.com/pion/logging"
	"github.com/pion/rtp"
	"math/rand"
	"sync"
	"time"
)

type Processor struct {
	twccHdrExtId   uint8
	twccHdrExtSet  bool
	twccRTCPWRiter interceptor.RTCPWriter

	interceptor.NoOp

	log logging.LeveledLogger

	m     sync.Mutex
	wg    sync.WaitGroup
	close chan struct{}

	interval  time.Duration
	startTime time.Time

	recorder   *Recorder
	packetChan chan packet
}

type CustomTWCCProcessor interface {
	Process(int, []byte, []interceptor.RTPHeaderExtension) (int, error)
	Processor() *Processor
	BindTWCCRTCPWriter(interceptor.RTCPWriter)
}

type NoopTWCCProcessor struct{}

func NewCustomTWCCProcessor() (CustomTWCCProcessor, error) {
	twccProcessor := &Processor{
		log:           logging.NewDefaultLoggerFactory().NewLogger("twcc_processor_interceptor"),
		packetChan:    make(chan packet, 50),
		close:         make(chan struct{}),
		interval:      100 * time.Millisecond,
		startTime:     time.Now(),
		twccHdrExtId:  0,
		twccHdrExtSet: false,
	}
	twccProcessor.processTWCCFeedback()
	return twccProcessor, nil
}

func (t *NoopTWCCProcessor) Processor() *Processor {
	return nil
}

func (t *NoopTWCCProcessor) processTWCCFeedback() {
	return
}

func (t *NoopTWCCProcessor) BindTWCCRTCPWriter(_ interceptor.RTCPWriter) {
	return
}

func (t *NoopTWCCProcessor) Process(size int, _ []byte, _ []interceptor.RTPHeaderExtension) (int, error) {
	return size, nil
}

func (t *Processor) Processor() *Processor {
	return t
}

func (t *Processor) BindTWCCRTCPWriter(writer interceptor.RTCPWriter) {
	t.twccRTCPWRiter = writer
}

func (t *Processor) Process(i int, buf []byte, ext []interceptor.RTPHeaderExtension) (int, error) {

	if !t.twccHdrExtSet {
		for _, e := range ext {
			if e.URI == transportCCURI {
				t.twccHdrExtId = uint8(e.ID) //nolint:gosec // G115
				t.twccHdrExtSet = true
				break
			}
		}
	}

	if t.twccHdrExtId == 0 {
		return 0, fmt.Errorf("no header extension found")
	}

	attr := make(interceptor.Attributes)

	header, err := attr.GetRTPHeader(buf[:i])
	if err != nil {
		return 0, err
	}
	var tccExt rtp.TransportCCExtension
	if ext := header.GetExtension(t.twccHdrExtId); ext != nil {
		err = tccExt.Unmarshal(ext)
		if err != nil {
			return 0, err
		}

		p := packet{
			hdr:            header,
			sequenceNumber: tccExt.TransportSequence,
			arrivalTime:    time.Since(t.startTime).Microseconds(),
			ssrc:           header.SSRC,
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

func (t *Processor) processTWCCFeedback() {
	t.m.Lock()
	defer t.m.Unlock()

	if t.recorder != nil {
		return
	}

	t.recorder = NewRecorder(rand.Uint32())

	if t.isClosed() {
		return
	}

	t.wg.Add(1)

	go t.loop()
}

func (t *Processor) isClosed() bool {
	select {
	case <-t.close:
		return true
	default:
		return false
	}
}

func (t *Processor) loop() {
	defer t.wg.Done()

	select {
	case <-t.close:
		return
	case p := <-t.packetChan:
		t.recorder.Record(p.ssrc, p.sequenceNumber, p.arrivalTime)
	}

	ticker := time.NewTicker(t.interval)
	for {
		select {
		case <-t.close:
			ticker.Stop()
			return
		case p := <-t.packetChan:
			t.recorder.Record(p.ssrc, p.sequenceNumber, p.arrivalTime)

		case <-ticker.C:
			pkts := t.recorder.BuildFeedbackPacket()
			if len(pkts) == 0 {
				continue
			}

			if t.twccRTCPWRiter == nil {
				continue
			}

			if _, err := t.twccRTCPWRiter.Write(pkts, nil); err != nil {
				t.log.Error(err.Error())
			}
		}
	}
}

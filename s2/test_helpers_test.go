package s2

type fakeAppendSession struct {
	nextSeq   uint64
	submitErr error
}

func (f *fakeAppendSession) Submit(input *AppendInput) (*SubmitFuture, error) {
	if f.submitErr != nil {
		return nil, f.submitErr
	}
	batchSize := len(input.Records)
	start := f.nextSeq
	f.nextSeq += uint64(batchSize)

	ack := &AppendAck{
		Start: StreamPosition{SeqNum: start},
		End:   StreamPosition{SeqNum: start + uint64(batchSize)},
		Tail:  StreamPosition{SeqNum: start + uint64(batchSize)},
	}

	ticketCh := make(chan *BatchSubmitTicket, 1)
	errCh := make(chan error, 1)
	ackCh := make(chan *inflightResult, 1)

	ticketCh <- &BatchSubmitTicket{ackCh: ackCh}
	go func() {
		ackCh <- &inflightResult{ack: ack}
		close(ackCh)
	}()

	return &SubmitFuture{ticketCh: ticketCh, errCh: errCh}, nil
}

func (f *fakeAppendSession) Close() error {
	return nil
}

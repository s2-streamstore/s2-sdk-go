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

	future := newSubmitFuture(nil)
	ackCh := make(chan *inflightResult, 1)

	future.accept(&BatchSubmitTicket{ackCh: ackCh})
	go func() {
		ackCh <- &inflightResult{ack: ack}
		close(ackCh)
	}()

	return future, nil
}

func (f *fakeAppendSession) Close() error {
	return nil
}

func testBasinBaseURL(string) string {
	return "http://basin.test"
}

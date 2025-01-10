package s2_test

import (
	"context"
	"os"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

var (
	myAuthToken    = os.Getenv("S2_AUTH_TOKEN")
	myBasin        = os.Getenv("S2_BASIN")
	myStream       = os.Getenv("S2_STREAM")
	myFencingToken = []byte("my-fencing-token")
)

func ExampleAppendRecordBatchingSender() {
	streamClient, err := s2.NewStreamClient(myBasin, myStream, myAuthToken)
	if err != nil {
		panic(err)
	}

	sender, _, err := streamClient.AppendSession(context.TODO())
	if err != nil {
		panic(err)
	}

	recordSender, err := s2.NewAppendRecordBatchingSender(
		sender,
		s2.WithFencingToken(myFencingToken),
		s2.WithMaxBatchRecords(100),
	)
	if err != nil {
		panic(err)
	}
	defer recordSender.Close()

	records := []s2.AppendRecord{
		{Body: []byte("my record 1")},
		{Body: []byte("my record 2")},
		// ...
	}

	for _, record := range records {
		if err := recordSender.Send(record); err != nil {
			panic(err)
		}
	}
}

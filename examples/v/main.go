package main

import (
	"context"
	"fmt"
	"os"

	"github.com/s2-streamstore/optr"
	"github.com/s2-streamstore/s2-sdk-go/s2"
)

func main() {
	client, err := s2.NewStreamClient("vrongmeal-basin", "s", os.Getenv("S2_AUTH_TOKEN"))
	if err != nil {
		panic(err)
	}

	// batch, _ := s2.NewAppendRecordBatch(
	// 	s2.AppendRecord{Body: []byte("hello")},
	// 	s2.AppendRecord{Body: []byte("bye")},
	// )

	// tx, rx, err := client.AppendSession(context.TODO())
	// if err != nil {
	// 	panic(err)
	// }

	// if err := tx.Send(&s2.AppendInput{Records: batch}); err != nil {
	// 	panic(err)
	// }

	// ack, err := rx.Recv()
	// if err != nil {
	// 	panic(err)
	// }

	// fmt.Printf("%#v\n", ack)

	tail, err := client.CheckTail(context.TODO())
	if err != nil {
		panic(err)
	}

	rx, err := client.ReadSession(context.TODO(), &s2.ReadSessionRequest{
		StartSeqNum: tail - 4,
		Limit: s2.ReadLimit{
			Count: optr.Some(uint64(4)),
		},
	})
	if err != nil {
		panic(err)
	}

	read, err := rx.Recv()
	if err != nil {
		panic(err)
	}

	for _, v := range read.(s2.ReadOutputBatch).Records {
		fmt.Println(string(v.Body))
	}
}

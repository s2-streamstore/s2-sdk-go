package s2

import (
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var errInvalidBatchBytes = errors.New("batch metered size must be more than 0 and less than 1 MiB")

func withMaxBatchBytes(n uint) AppendRecordBatchingConfigParam {
	return applyAppendRecordBatchingConfigParamFunc(func(arbc *appendRecordBatchingConfig) error {
		if n == 0 || n > MaxBatchBytes {
			return errInvalidBatchBytes
		}

		arbc.MaxBatchBytes = n

		return nil
	})
}

type testAppendSessionSender struct {
	inputs atomic.Value
	closed atomic.Bool
}

func (s *testAppendSessionSender) Inputs(t *testing.T) []*AppendInput {
	inputs, ok := s.inputs.Load().([]*AppendInput)
	require.True(t, ok)

	return inputs
}

func (s *testAppendSessionSender) Send(input *AppendInput) error {
	if s.closed.Load() {
		return ErrAppendRecordSenderClosed
	}

	if inputs, ok := s.inputs.Load().([]*AppendInput); ok {
		inputs = append(inputs, input)
		s.inputs.Store(inputs)
	} else {
		s.inputs.Store([]*AppendInput{input})
	}

	return nil
}

func (s *testAppendSessionSender) Close() error {
	s.closed.Store(true)

	return nil
}

func TestAppendRecordBatchingMechanics(t *testing.T) {
	testCases := []struct {
		MaxBatchRecords uint
		MaxBatchBytes   uint
	}{
		{
			MaxBatchRecords: 2,
		},
		{
			MaxBatchBytes: 30,
		},
		{
			MaxBatchRecords: 2,
			MaxBatchBytes:   100,
		},
		{
			MaxBatchRecords: 10,
			MaxBatchBytes:   30,
		},
	}

	for _, testCase := range testCases {
		t.Run(
			fmt.Sprintf("records-%d-bytes-%d", testCase.MaxBatchRecords, testCase.MaxBatchBytes),
			func(t *testing.T) {
				batchSender := testAppendSessionSender{}

				var params []AppendRecordBatchingConfigParam
				if testCase.MaxBatchRecords != 0 {
					params = append(params, WithMaxBatchRecords(testCase.MaxBatchRecords))
				}

				if testCase.MaxBatchBytes != 0 {
					params = append(params, withMaxBatchBytes(testCase.MaxBatchBytes))
				}

				recordSender, err := NewAppendRecordBatchingSender(&batchSender, params...)
				require.NoError(t, err)

				for i := range 100 {
					body := fmt.Sprintf("r_%d", i)
					require.NoError(t, recordSender.Send(AppendRecord{Body: []byte(body)}))
				}

				require.NoError(t, recordSender.Close())

				i := 0

				for _, input := range batchSender.Inputs(t) {
					require.Equal(t, uint(2), input.Records.Len())

					for _, record := range input.Records.Records() {
						require.Equal(t, string(record.Body), fmt.Sprintf("r_%d", i))

						i++
					}
				}
			},
		)
	}
}

// TODO: This test takes time to run since we don't really mock time here.
// There is no easy way to mock go runtime's time so we need the batching
// sender to accept a clock interface.
func TestAppendRecordBatchingLinger(t *testing.T) {
	var (
		i           uint
		sizeLimit   uint = 40
		batchSender testAppendSessionSender
	)

	recordSender, err := NewAppendRecordBatchingSender(
		&batchSender,
		WithLinger(2*time.Second),
		WithMaxBatchRecords(3),
		withMaxBatchBytes(sizeLimit),
	)
	require.NoError(t, err)

	sendNext := func(padding string) {
		record := AppendRecord{Body: []byte(fmt.Sprintf("r_%d", i))}
		if padding != "" {
			record.Headers = append(record.Headers, Header{
				Name:  []byte("padding"),
				Value: []byte(padding),
			})
		}

		require.NoError(t, recordSender.Send(record))

		i++
	}

	sleepForSeconds := func(seconds uint) {
		nanos := seconds * uint(time.Second)
		nanos += uint(time.Millisecond)
		time.Sleep(time.Duration(nanos))
	}

	sendNext("")
	sendNext("")

	sleepForSeconds(2)

	sendNext("")

	sleepForSeconds(1)

	sendNext("")

	sleepForSeconds(1)

	sendNext("")
	sendNext("")
	sendNext("")
	sendNext("")

	sleepForSeconds(10)

	sendNext("large string")
	sendNext("")

	require.NoError(t, recordSender.Close())

	expectedBatches := [][]string{
		{"r_0", "r_1"},
		{"r_2", "r_3"},
		{"r_4", "r_5", "r_6"},
		{"r_7"},
		{"r_8"},
		{"r_9"},
	}

	actualBatches := batchSender.Inputs(t)

	require.Equal(t, len(expectedBatches), len(actualBatches))

	for i, input := range actualBatches {
		expectedBatch := expectedBatches[i]
		actualBatch := input.Records.Records()

		require.Equal(t, len(expectedBatch), len(actualBatch))

		for i, record := range actualBatch {
			require.Equal(t, expectedBatch[i], string(record.Body))
		}
	}
}

func TestAppendRecordBatchingErrorSizeLimits(t *testing.T) {
	record := AppendRecord{
		Body: []byte("too long to fit in size limits"),
	}

	var batchSender testAppendSessionSender

	recordSender, err := NewAppendRecordBatchingSender(&batchSender, withMaxBatchBytes(1))
	require.NoError(t, err)

	require.ErrorIs(t, recordSender.Send(record), ErrRecordTooBig)
	require.ErrorIs(t, recordSender.Close(), ErrRecordTooBig)
}

func TestAppendRecordBatchingAppendInputOpts(t *testing.T) {
	testRecord := AppendRecord{Body: []byte("a")}

	totalRecords := uint(12)

	records := make([]AppendRecord, 0, totalRecords)

	for range totalRecords {
		records = append(records, testRecord)
	}

	expectedFencingToken := []byte("hello")
	expectedMatchSeqNum := uint64(10)

	numBatchRecords := uint(3)

	var batchSender testAppendSessionSender

	recordSender, err := NewAppendRecordBatchingSender(
		&batchSender,
		WithFencingToken(expectedFencingToken),
		WithMatchSeqNum(expectedMatchSeqNum),
		WithMaxBatchRecords(numBatchRecords),
	)
	require.NoError(t, err)

	for _, record := range records {
		require.NoError(t, recordSender.Send(record))
	}

	require.NoError(t, recordSender.Close())

	batches := batchSender.Inputs(t)

	require.Equal(t, totalRecords/numBatchRecords, uint(len(batches)))

	expectedRecords := make([]AppendRecord, 0, numBatchRecords)
	for range numBatchRecords {
		expectedRecords = append(expectedRecords, testRecord)
	}

	for _, input := range batches {
		require.Equal(t, expectedRecords, input.Records.Records())
		require.Equal(t, expectedFencingToken, input.FencingToken)
		require.NotNil(t, input.MatchSeqNum)
		require.Equal(t, expectedMatchSeqNum, *input.MatchSeqNum)
		expectedMatchSeqNum += uint64(numBatchRecords)
	}
}

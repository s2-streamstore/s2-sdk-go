package s2

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

var errInvalidBatchBytes = errors.New("batch metered size must be more than 0 and less than 1 MiB")

func withMaxBatchBytes(n uint) AppendRecordBatchingConfigParam {
	return applyAppendRecordBatchingConfigParamFunc(func(arbc *appendRecordBatchingConfig) error {
		if n == 0 || n > mibBytes {
			return errInvalidBatchBytes
		}

		arbc.MaxBatchBytes = n

		return nil
	})
}

type testAppendSessionSender struct {
	Inputs []*AppendInput
}

func (t *testAppendSessionSender) Send(input *AppendInput) error {
	t.Inputs = append(t.Inputs, input)

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
					params = append(params, WithMaxRecordsInBatch(testCase.MaxBatchRecords))
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

				for _, input := range batchSender.Inputs {
					require.Len(t, input.Records, 2)

					for _, record := range input.Records {
						require.Equal(t, string(record.Body), fmt.Sprintf("r_%d", i))

						i++
					}
				}
			},
		)
	}
}

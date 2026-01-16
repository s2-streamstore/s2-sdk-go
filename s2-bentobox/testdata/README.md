# S2 Bento Plugin Tests

Manual integration tests for the S2 Bento plugin.

## Prerequisites

1. An S2 account at [s2.dev](https://s2.dev)
2. A basin and stream created in the S2 dashboard
3. An access token

## Setup

```bash
# Set your S2 access token
export S2_ACCESS_TOKEN="your-token-here"

# Build bento with the local s2-sdk-go
cd ../bento
go mod edit -replace github.com/s2-streamstore/s2-sdk-go=../s2-sdk-go-old
go mod tidy
go build -o bento ./cmd/bento
```

## Running Tests

### Test Output (Write to S2)

Edit `test-output.yaml` to set your basin and stream, then:

```bash
cd ../bento
./bento run ../s2-sdk-go-old/s2-bentobox/testdata/test-output.yaml
```

This generates 3 test messages and writes them to your S2 stream.

### Test Input (Read from S2)

Edit `test-input.yaml` to set your basin and stream, then:

```bash
cd ../bento
./bento run ../s2-sdk-go-old/s2-bentobox/testdata/test-input.yaml
```

This reads messages from your S2 stream and prints them to stdout. Press Ctrl+C to stop.

## Test Files

- `test-output.yaml` - Generates messages and writes to S2
- `test-input.yaml` - Reads from S2 and prints to stdout

## Troubleshooting

- **"session closed" error at shutdown**: Benign if `queue_len=0` - writes completed successfully
- **Authentication errors**: Verify your `S2_ACCESS_TOKEN` is correct
- **Stream not found**: Ensure the basin and stream exist in your S2 account

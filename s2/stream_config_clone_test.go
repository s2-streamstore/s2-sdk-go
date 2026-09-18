package s2

import "testing"

// fullStreamConfigForCloneTest builds a StreamConfig with every nested pointer
// field populated so the deep-clone tests exercise the full tree.
func fullStreamConfigForCloneTest() *StreamConfig {
	storage := StorageClassExpress
	mode := TimestampingModeClientRequire
	return &StreamConfig{
		DeleteOnEmpty:   &DeleteOnEmptyConfig{MinAgeSecs: Int64(300)},
		RetentionPolicy: &RetentionPolicy{Age: Int64(3600), Infinite: &InfiniteRetention{}},
		StorageClass:    &storage,
		Timestamping:    &TimestampingConfig{Mode: &mode, Uncapped: Bool(true)},
	}
}

func TestCloneStreamConfigPtr_Nil(t *testing.T) {
	if got := cloneStreamConfigPtr(nil); got != nil {
		t.Fatalf("expected nil clone for nil input, got %+v", got)
	}
}

func TestCloneStreamConfigPtr_DeepCopy(t *testing.T) {
	src := fullStreamConfigForCloneTest()
	clone := cloneStreamConfigPtr(src)

	// Top-level and nested pointers must be distinct allocations so the SDK
	// never retains references to caller-owned mutable state.
	if clone == src {
		t.Fatal("top-level StreamConfig pointer is shared with source")
	}
	if clone.DeleteOnEmpty == src.DeleteOnEmpty {
		t.Fatal("DeleteOnEmpty pointer is shared with source")
	}
	if clone.RetentionPolicy == src.RetentionPolicy {
		t.Fatal("RetentionPolicy pointer is shared with source")
	}
	if clone.StorageClass == src.StorageClass {
		t.Fatal("StorageClass pointer is shared with source")
	}
	if clone.Timestamping == src.Timestamping {
		t.Fatal("Timestamping pointer is shared with source")
	}
	if clone.DeleteOnEmpty.MinAgeSecs == src.DeleteOnEmpty.MinAgeSecs {
		t.Fatal("DeleteOnEmpty.MinAgeSecs pointer is shared with source")
	}
	if clone.RetentionPolicy.Age == src.RetentionPolicy.Age {
		t.Fatal("RetentionPolicy.Age pointer is shared with source")
	}
	if clone.Timestamping.Mode == src.Timestamping.Mode {
		t.Fatal("Timestamping.Mode pointer is shared with source")
	}
	if clone.Timestamping.Uncapped == src.Timestamping.Uncapped {
		t.Fatal("Timestamping.Uncapped pointer is shared with source")
	}

	// InfiniteRetention is an empty struct; sharing it is harmless, but the
	// clone must preserve its presence.
	if clone.RetentionPolicy.Infinite == nil {
		t.Fatal("RetentionPolicy.Infinite lost during clone (expected non-nil)")
	}

	// Values must round-trip equal.
	if *clone.DeleteOnEmpty.MinAgeSecs != *src.DeleteOnEmpty.MinAgeSecs {
		t.Fatalf("MinAgeSecs value mismatch: clone=%d src=%d", *clone.DeleteOnEmpty.MinAgeSecs, *src.DeleteOnEmpty.MinAgeSecs)
	}
	if *clone.RetentionPolicy.Age != *src.RetentionPolicy.Age {
		t.Fatalf("Retention age value mismatch: clone=%d src=%d", *clone.RetentionPolicy.Age, *src.RetentionPolicy.Age)
	}
	if *clone.StorageClass != *src.StorageClass {
		t.Fatalf("StorageClass value mismatch: clone=%s src=%s", *clone.StorageClass, *src.StorageClass)
	}
	if *clone.Timestamping.Mode != *src.Timestamping.Mode {
		t.Fatalf("Timestamping mode value mismatch: clone=%s src=%s", *clone.Timestamping.Mode, *src.Timestamping.Mode)
	}
	if *clone.Timestamping.Uncapped != *src.Timestamping.Uncapped {
		t.Fatalf("Timestamping uncapped value mismatch: clone=%v src=%v", *clone.Timestamping.Uncapped, *src.Timestamping.Uncapped)
	}

	// Mutating the clone must not affect the source (and vice versa).
	*clone.DeleteOnEmpty.MinAgeSecs = 1
	*clone.RetentionPolicy.Age = 2
	*clone.StorageClass = StorageClassStandard
	*clone.Timestamping.Mode = TimestampingModeArrival
	*clone.Timestamping.Uncapped = false
	if *src.DeleteOnEmpty.MinAgeSecs != 300 {
		t.Fatalf("source MinAgeSecs mutated by clone write: got %d want 300", *src.DeleteOnEmpty.MinAgeSecs)
	}
	if *src.RetentionPolicy.Age != 3600 {
		t.Fatalf("source retention age mutated by clone write: got %d want 3600", *src.RetentionPolicy.Age)
	}
	if *src.StorageClass != StorageClassExpress {
		t.Fatalf("source StorageClass mutated by clone write: got %s", *src.StorageClass)
	}
	if *src.Timestamping.Mode != TimestampingModeClientRequire {
		t.Fatalf("source Timestamping.Mode mutated by clone write: got %s", *src.Timestamping.Mode)
	}
	if !*src.Timestamping.Uncapped {
		t.Fatal("source Timestamping.Uncapped mutated by clone write: got false want true")
	}
}

func TestCloneStreamConfigPtr_PartialFieldsStayNil(t *testing.T) {
	src := &StreamConfig{RetentionPolicy: &RetentionPolicy{Age: Int64(3600)}}
	clone := cloneStreamConfigPtr(src)

	if clone.DeleteOnEmpty != nil {
		t.Fatalf("expected nil DeleteOnEmpty, got %+v", clone.DeleteOnEmpty)
	}
	if clone.StorageClass != nil {
		t.Fatalf("expected nil StorageClass, got %v", *clone.StorageClass)
	}
	if clone.Timestamping != nil {
		t.Fatalf("expected nil Timestamping, got %+v", clone.Timestamping)
	}
	if clone.RetentionPolicy == nil {
		t.Fatal("expected non-nil RetentionPolicy")
	}
	if clone.RetentionPolicy.Infinite != nil {
		t.Fatalf("expected nil RetentionPolicy.Infinite, got %+v", clone.RetentionPolicy.Infinite)
	}
	if clone.RetentionPolicy.Age == nil || *clone.RetentionPolicy.Age != 3600 {
		t.Fatalf("expected cloned retention age 3600, got %v", clone.RetentionPolicy.Age)
	}
}

func TestCloneAppendInput_DeepClonesStreamConfig(t *testing.T) {
	src := &AppendInput{
		Records:      []AppendRecord{{Body: []byte("x")}},
		StreamConfig: &StreamConfig{RetentionPolicy: &RetentionPolicy{Age: Int64(3600)}},
	}
	clone := cloneAppendInput(src)

	if clone.StreamConfig == src.StreamConfig {
		t.Fatal("cloneAppendInput shares StreamConfig pointer with source")
	}
	if clone.StreamConfig.RetentionPolicy == src.StreamConfig.RetentionPolicy {
		t.Fatal("cloneAppendInput shares RetentionPolicy pointer with source")
	}
	if clone.StreamConfig.RetentionPolicy.Age == src.StreamConfig.RetentionPolicy.Age {
		t.Fatal("cloneAppendInput shares RetentionPolicy.Age pointer with source")
	}

	// Mutating the clone must not affect the source.
	*clone.StreamConfig.RetentionPolicy.Age = 9999
	if *src.StreamConfig.RetentionPolicy.Age != 3600 {
		t.Fatalf("source retention age mutated by clone write: got %d want 3600", *src.StreamConfig.RetentionPolicy.Age)
	}
}

func TestCloneReadSessionOptions_DeepClonesStreamConfig(t *testing.T) {
	opts := &ReadOptions{
		Count:        Uint64(5),
		StreamConfig: &StreamConfig{RetentionPolicy: &RetentionPolicy{Age: Int64(3600)}},
	}
	clone := cloneReadSessionOptions(opts)

	if clone.StreamConfig == opts.StreamConfig {
		t.Fatal("cloneReadSessionOptions shares StreamConfig pointer with source")
	}
	if clone.StreamConfig.RetentionPolicy == opts.StreamConfig.RetentionPolicy {
		t.Fatal("cloneReadSessionOptions shares RetentionPolicy pointer with source")
	}
	if clone.StreamConfig.RetentionPolicy.Age == opts.StreamConfig.RetentionPolicy.Age {
		t.Fatal("cloneReadSessionOptions shares RetentionPolicy.Age pointer with source")
	}

	// Mutating the clone must not affect the source.
	*clone.StreamConfig.RetentionPolicy.Age = 9999
	if *opts.StreamConfig.RetentionPolicy.Age != 3600 {
		t.Fatalf("source retention age mutated by clone write: got %d want 3600", *opts.StreamConfig.RetentionPolicy.Age)
	}

	// Existing deep-cloned fields must remain isolated too (no regression).
	*clone.Count = 99
	if *opts.Count != 5 {
		t.Fatalf("source Count mutated by clone write: got %d want 5", *opts.Count)
	}
}

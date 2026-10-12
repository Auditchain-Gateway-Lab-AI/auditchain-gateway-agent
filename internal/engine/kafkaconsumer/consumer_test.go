package kafkaconsumer

import (
	"context"
	"errors"
	"testing"

	"github.com/segmentio/kafka-go"
)

type testCommitter struct {
	calls int
	err   error
}

func (c *testCommitter) CommitMessages(_ context.Context, _ ...kafka.Message) error {
	c.calls++
	return c.err
}

func TestParseKafkaStartOffset(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    int64
		wantErr bool
	}{
		{name: "earliest", value: "earliest", want: kafka.FirstOffset},
		{name: "latest case insensitive", value: " LATEST ", want: kafka.LastOffset},
		{name: "missing is rejected", wantErr: true},
		{name: "invalid is rejected", value: "2", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseKafkaStartOffset(tt.value)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseKafkaStartOffset() error = %v, wantErr=%v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("parseKafkaStartOffset() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestKafkaMessageLogIDIsStableAndOffsetSpecific(t *testing.T) {
	message := kafka.Message{Topic: "morbis_simrs.POLINEMA.AGAMA", Partition: 2, Offset: 41}
	if first, second := kafkaMessageLogID("client-a", message), kafkaMessageLogID("client-a", message); first != second {
		t.Fatalf("message ID is not stable: %q != %q", first, second)
	}

	otherOffset := message
	otherOffset.Offset++
	if kafkaMessageLogID("client-a", message) == kafkaMessageLogID("client-a", otherOffset) {
		t.Fatal("different Kafka offsets produced the same message ID")
	}
	otherPartition := message
	otherPartition.Partition++
	if kafkaMessageLogID("client-a", message) == kafkaMessageLogID("client-a", otherPartition) {
		t.Fatal("different Kafka partitions produced the same message ID")
	}
	otherTopic := message
	otherTopic.Topic += ".other"
	if kafkaMessageLogID("client-a", message) == kafkaMessageLogID("client-a", otherTopic) {
		t.Fatal("different Kafka topics produced the same message ID")
	}
	if kafkaMessageLogID("client-a", message) == kafkaMessageLogID("client-b", message) {
		t.Fatal("different clients produced the same message ID")
	}
}

func TestProcessAndCommitDoesNotCommitWhenProcessingFails(t *testing.T) {
	committer := &testCommitter{}
	wantErr := errors.New("database transaction failed")
	err := processAndCommit(context.Background(), committer, kafka.Message{Topic: "cdc", Offset: 7}, func(kafka.Message) error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("processAndCommit() error = %v, want wrapped processing error", err)
	}
	if committer.calls != 0 {
		t.Fatalf("CommitMessages called %d times after processing failure", committer.calls)
	}
}

func TestProcessAndCommitCommitsAfterSuccessfulProcessing(t *testing.T) {
	committer := &testCommitter{}
	processed := false
	err := processAndCommit(context.Background(), committer, kafka.Message{Topic: "cdc", Offset: 7}, func(kafka.Message) error {
		processed = true
		return nil
	})
	if err != nil {
		t.Fatalf("processAndCommit() error = %v", err)
	}
	if !processed || committer.calls != 1 {
		t.Fatalf("processed=%v, commits=%d; want processed once and one commit", processed, committer.calls)
	}
}

func TestProcessAndCommitReturnsCommitFailure(t *testing.T) {
	wantErr := errors.New("broker unavailable")
	committer := &testCommitter{err: wantErr}
	err := processAndCommit(context.Background(), committer, kafka.Message{Topic: "cdc", Offset: 7}, func(kafka.Message) error {
		return nil
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("processAndCommit() error = %v, want wrapped commit error", err)
	}
	if committer.calls != 1 {
		t.Fatalf("CommitMessages called %d times, want 1", committer.calls)
	}
}

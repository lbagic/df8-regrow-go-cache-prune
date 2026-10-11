package protocol

import (
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func collectLines(t *testing.T, r io.Reader, max int) ([]requestLine, error) {
	t.Helper()
	var got []requestLine
	err := readLines(r, max, func(l requestLine) bool {
		got = append(got, l)
		return true
	})
	return got, err
}

func TestReadLinesBoundsEachLine(t *testing.T) {
	const max = 8
	// The reader's buffer is far larger than max and far smaller than
	// the long line, so the limit is enforced across buffer refills.
	long := strings.Repeat("y", 200_000)
	input := "12345678\n" + "123456789\n" + long + "\n" + "\n" + "ok\n" + "tail"
	got, err := collectLines(t, strings.NewReader(input), max)
	if err != nil {
		t.Fatal(err)
	}
	want := []requestLine{
		{data: []byte("12345678")},
		{tooLong: true},
		{tooLong: true},
		{},
		{data: []byte("ok")},
		{data: []byte("tail")},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if string(got[i].data) != string(want[i].data) || got[i].tooLong != want[i].tooLong {
			t.Errorf("line %d = {%q tooLong=%v}, want {%q tooLong=%v}", i, got[i].data, got[i].tooLong, want[i].data, want[i].tooLong)
		}
	}
}

func TestReadLinesSurvivesByteAtATimeInput(t *testing.T) {
	got, err := collectLines(t, iotest.OneByteReader(strings.NewReader("ab\ncdefg\nh\n")), 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || string(got[0].data) != "ab" || !got[1].tooLong || string(got[2].data) != "h" {
		t.Fatalf("lines = %+v", got)
	}
}

func TestReadLinesReturnsTheReadError(t *testing.T) {
	broken := errors.New("pipe broke")
	got, err := collectLines(t, io.MultiReader(strings.NewReader("a\nb"), iotest.ErrReader(broken)), 8)
	if !errors.Is(err, broken) {
		t.Fatalf("err = %v, want the reader's error", err)
	}
	if len(got) != 2 || string(got[1].data) != "b" {
		t.Fatalf("lines before the error = %+v", got)
	}
}

func TestReadLinesStopsWhenDeliverDeclines(t *testing.T) {
	n := 0
	err := readLines(strings.NewReader("a\nb\nc\n"), 8, func(requestLine) bool {
		n++
		return n < 2
	})
	if err != nil || n != 2 {
		t.Fatalf("delivered %d lines (err %v), want reading to stop at the second", n, err)
	}
}

package arena

import "testing"

func BenchmarkAppend3(b *testing.B) {
	s := Make[uint64](3)
	v := []uint64{1, 2, 3}
	for i := 0; i < b.N; i++ {
		s.Append(v...)
	}
}

func BenchmarkAppend9(b *testing.B) {
	s := Make[uint64](9)
	v := []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9}
	for i := 0; i < b.N; i++ {
		s.Append(v...)
	}
}

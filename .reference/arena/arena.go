// Package arena stores per-node search data in fixed-size chunks. Unlike an
// appended slice, growing never copies: a slice doubling from 400 MB holds
// both arrays at once (and leaves the old one as garbage), which is where most
// of our RSS overshoot above the tracked budget came from.
package arena

const shift = 16
const per = 1 << shift // items per chunk

// Slice holds items of W elements each, addressed by int32 index.
type Slice[T any] struct {
	chunks [][]T
	n      int
	w      int
}

// Make returns an empty arena of items with w elements each.
func Make[T any](w int) Slice[T] { return Slice[T]{w: w} }

// Len is the number of items.
func (s *Slice[T]) Len() int { return s.n }

// Item returns item i's elements (capacity-limited, so appends to it can't
// overwrite the next item).
func (s *Slice[T]) Item(i int32) []T {
	c := s.chunks[i>>shift]
	o := int(i&(per-1)) * s.w
	return c[o : o+s.w : o+s.w]
}

// At returns element 0 of item i (for one-element items).
func (s *Slice[T]) At(i int32) T { return s.chunks[i>>shift][int(i&(per-1))*s.w] }

// Set sets element 0 of item i.
func (s *Slice[T]) Set(i int32, v T) { s.chunks[i>>shift][int(i&(per-1))*s.w] = v }

// Append adds one item; v must have w elements.
func (s *Slice[T]) Append(v ...T) {
	if s.n>>shift == len(s.chunks) {
		s.chunks = append(s.chunks, make([]T, per*s.w))
	}
	copy(s.Item(int32(s.n)), v)
	s.n++
}

// Elems is the number of elements allocated (for memory accounting:
// multiply by the element size).
func (s *Slice[T]) Elems() int { return len(s.chunks) * per * s.w }

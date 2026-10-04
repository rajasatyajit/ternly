package shapes

// Shape is anything with an area and a name.
type Shape interface {
	Area() float64
	Name() string
}

type base struct{ ID int }

// Square implements Shape (Area on the pointer, Name on the value).
type Square struct {
	base
	Side float64
}

func (s *Square) Area() float64 { return s.Side * s.Side }

func (s Square) Name() string { return "square" }

// Circle has an Area but no Name: not a Shape.
type Circle struct{ R float64 }

func (c Circle) Area() float64 { return 3 * c.R * c.R }

func (c Circle) String() string { return "circle" }

func NewSquare(n float64) *Square { return &Square{Side: n} }

func Total(ss []Shape) float64 {
	t := 0.0
	for _, s := range ss {
		t += s.Area()
	}
	return t
}

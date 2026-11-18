package chartdata

// FrameType names a frame's type on the wire, so a chart can check that its
// mark fits the frame and a set event can carry any frame.
type FrameType string

// The frame types, one per data family the chart elements draw.
const (
	TypeSeries     FrameType = "series"
	TypeBins       FrameType = "bins"
	TypeCategories FrameType = "categories"
	TypeBoxes      FrameType = "boxes"
	TypePoints     FrameType = "points"
	TypeGrid       FrameType = "grid"
	TypeCalendar   FrameType = "calendar"
	TypeTree       FrameType = "tree"
	TypeFlow       FrameType = "flow"
	TypeEvents     FrameType = "events"
	TypeStates     FrameType = "states"
	TypeValue      FrameType = "value"
)

// FrameAxis says what a BinsFrame's bins lie along.
type FrameAxis string

// The axes a BinsFrame's bins can lie along.
const (
	// AxisTime is instants, in Unix milliseconds: a column of a time series.
	AxisTime FrameAxis = "time"
	// AxisValue is values in the frame's unit: a histogram's buckets.
	AxisValue FrameAxis = "value"
)

// FrameRef is a reference value the server computed, such as a median, a
// p95 or a threshold, drawn as a rule. Key is data; the page holds its words.
type FrameRef struct {
	Key   string  `json:"key"`
	Value float64 `json:"value"`
}

// FrameNote is an event at a place on the x axis, such as a deploy or a
// restart, drawn as a text mark. At is an instant in Unix milliseconds on a
// time axis and a value otherwise.
type FrameNote struct {
	Key string  `json:"key"`
	At  float64 `json:"at"`
}

// FrameSeries is one series of a frame: its key, a label value or the
// declaration's name, and a value per x, null for a gap.
type FrameSeries struct {
	Key    string     `json:"key"`
	Values []*float64 `json:"values"`
}

// SeriesFrame is series of values along x, drawn as a line, an area or a
// sparkline. X holds instants in Unix milliseconds or plain numbers, strictly
// increasing, and every series holds a value per x.
type SeriesFrame struct {
	Type   FrameType     `json:"type" wiregen:"const=series"`
	Name   string        `json:"name"`
	Unit   string        `json:"unit"`
	X      []float64     `json:"x"`
	Series []FrameSeries `json:"series"`
	// Domain is the span the x axis covers, from and to, when it is wider
	// than the data, such as a window whose first hours hold no samples.
	Domain []float64 `json:"domain,omitempty"`
	// Resets are the instants a restart broke the series at: a line breaks
	// there instead of joining across them.
	Resets []int64     `json:"resets,omitempty"`
	Refs   []FrameRef  `json:"refs,omitempty"`
	Notes  []FrameNote `json:"notes,omitempty"`
	// Open says the last column is still filling.
	Open bool `json:"open,omitempty"`
	// Read is how the read behind the frame went, absent when it was whole.
	Read *FrameRead `json:"read,omitempty"`
}

// BinsFrame is series of values over bins from X1 to X2, drawn as rects or
// a histogram. X1 strictly increases, X2 never decreases, and each bin's X1
// is below its X2.
type BinsFrame struct {
	Type   FrameType     `json:"type" wiregen:"const=bins"`
	Name   string        `json:"name"`
	Unit   string        `json:"unit"`
	Axis   FrameAxis     `json:"axis"`
	X1     []float64     `json:"x1"`
	X2     []float64     `json:"x2"`
	Series []FrameSeries `json:"series"`
	// Domain is the span the axis covers, from and to.
	Domain []float64   `json:"domain,omitempty"`
	Resets []int64     `json:"resets,omitempty"`
	Refs   []FrameRef  `json:"refs,omitempty"`
	Notes  []FrameNote `json:"notes,omitempty"`
	// Open says the last bin is still filling.
	Open bool `json:"open,omitempty"`
	// Read is how the read behind the frame went, absent when it was whole.
	Read *FrameRead `json:"read,omitempty"`
}

// CategoriesFrame is a value per category in each series, drawn as bars,
// points, a slope (two names), a pie or symbols (one series), a share bar or
// ranked bars. Names are unique and never empty.
type CategoriesFrame struct {
	Type   FrameType     `json:"type" wiregen:"const=categories"`
	Name   string        `json:"name"`
	Unit   string        `json:"unit"`
	Names  []string      `json:"names"`
	Series []FrameSeries `json:"series"`
	Refs   []FrameRef    `json:"refs,omitempty"`
	Notes  []FrameNote   `json:"notes,omitempty"`
	// Read is how the read behind the frame went, absent when it was whole.
	Read *FrameRead `json:"read,omitempty"`
}

// FrameBoxes is one series of a BoxesFrame: each category's five numbers,
// all null where the category has no box, and the values beyond its
// whiskers.
type FrameBoxes struct {
	Key    string     `json:"key"`
	Low    []*float64 `json:"low"`
	Q1     []*float64 `json:"q1"`
	Median []*float64 `json:"median"`
	Q3     []*float64 `json:"q3"`
	High   []*float64 `json:"high"`
	// Outliers holds an array per category when any category has one.
	Outliers [][]float64 `json:"outliers,omitempty"`
}

// BoxesFrame is a distribution per category in each series, drawn as box
// plots. The server computes the five numbers; the chart computes none.
type BoxesFrame struct {
	Type   FrameType    `json:"type" wiregen:"const=boxes"`
	Name   string       `json:"name"`
	Unit   string       `json:"unit"`
	Names  []string     `json:"names"`
	Series []FrameBoxes `json:"series"`
	Refs   []FrameRef   `json:"refs,omitempty"`
	Notes  []FrameNote  `json:"notes,omitempty"`
	// Read is how the read behind the frame went, absent when it was whole.
	Read *FrameRead `json:"read,omitempty"`
}

// FrameUnits are a PointsFrame's units, one per measure.
type FrameUnits struct {
	X string `json:"x"`
	Y string `json:"y"`
	// R is set when the points carry a size.
	R string `json:"r,omitempty"`
}

// FramePoints is one group of a PointsFrame's points: each point's name,
// unique across the frame, and its x, y and, on every group or none, r.
type FramePoints struct {
	Key   string     `json:"key"`
	Names []string   `json:"names"`
	X     []*float64 `json:"x"`
	Y     []*float64 `json:"y"`
	R     []*float64 `json:"r,omitempty"`
}

// PointsFrame is items measured on two or three declarations, drawn as a
// scatter or bubble chart.
type PointsFrame struct {
	Type   FrameType     `json:"type" wiregen:"const=points"`
	Name   string        `json:"name"`
	Units  FrameUnits    `json:"units"`
	Series []FramePoints `json:"series"`
	Refs   []FrameRef    `json:"refs,omitempty"`
	Notes  []FrameNote   `json:"notes,omitempty"`
	// Read is how the read behind the frame went, absent when it was whole.
	Read *FrameRead `json:"read,omitempty"`
}

// FrameGridAxis is one axis of a GridFrame: exactly one of its categories'
// names or its bins' edges, one more edge than bins.
type FrameGridAxis struct {
	Names []string  `json:"names,omitempty"`
	Edges []float64 `json:"edges,omitempty"`
}

// GridFrame is a grid of cells, drawn as a cell grid or a 2D histogram:
// a row of values per y entry, each as long as x's names or bins.
type GridFrame struct {
	Type   FrameType     `json:"type" wiregen:"const=grid"`
	Name   string        `json:"name"`
	Unit   string        `json:"unit"`
	X      FrameGridAxis `json:"x"`
	Y      FrameGridAxis `json:"y"`
	Values [][]*float64  `json:"values"`
	Refs   []FrameRef    `json:"refs,omitempty"`
	Notes  []FrameNote   `json:"notes,omitempty"`
	// Read is how the read behind the frame went, absent when it was whole.
	Read *FrameRead `json:"read,omitempty"`
}

// CalendarFrame is a value per calendar day, drawn as a calendar heatmap.
// Start is the first instant of the first day in Zone, in Unix milliseconds,
// and each value is the next calendar day, 23 or 25 hours long across a DST
// change.
type CalendarFrame struct {
	Type FrameType `json:"type" wiregen:"const=calendar"`
	Name string    `json:"name"`
	Unit string    `json:"unit"`
	// Zone is the IANA time zone the days are in.
	Zone   string      `json:"zone"`
	Values []*float64  `json:"values"`
	Refs   []FrameRef  `json:"refs,omitempty"`
	Notes  []FrameNote `json:"notes,omitempty"`
	Start  int64       `json:"start"`
	// Open says the last day is still running.
	Open bool `json:"open,omitempty"`
	// Read is how the read behind the frame went, absent when it was whole.
	Read *FrameRead `json:"read,omitempty"`
}

// FrameTreePart is a part of a TreeFrame's whole: 0 or more, or null for no
// data.
type FrameTreePart struct {
	Value *float64 `json:"value"`
	Key   string   `json:"key"`
}

// FrameTreeGroup is a group of a TreeFrame's parts, its value their sum.
type FrameTreeGroup struct {
	Key      string          `json:"key"`
	Children []FrameTreePart `json:"children"`
}

// TreeFrame is parts of one whole, drawn as a treemap: one level of parts or
// two of groups and their parts, never both, in the order to lay them out.
// Siblings' keys are unique.
type TreeFrame struct {
	Type   FrameType        `json:"type" wiregen:"const=tree"`
	Name   string           `json:"name"`
	Unit   string           `json:"unit"`
	Parts  []FrameTreePart  `json:"parts,omitempty"`
	Groups []FrameTreeGroup `json:"groups,omitempty"`
	Refs   []FrameRef       `json:"refs,omitempty"`
	Notes  []FrameNote      `json:"notes,omitempty"`
	// Read is how the read behind the frame went, absent when it was whole.
	Read *FrameRead `json:"read,omitempty"`
}

// FrameFlowNode is a FlowFrame's node: a name in a Sankey diagram, a
// category at every stage in an alluvial one.
type FrameFlowNode struct {
	Key string `json:"key"`
}

// FrameFlowLink is an amount flowing from one node to another, null for no
// data. In an alluvial diagram Stage names the stage the link leaves.
type FrameFlowLink struct {
	Value *float64 `json:"value"`
	From  string   `json:"from"`
	To    string   `json:"to"`
	Stage string   `json:"stage,omitempty"`
}

// FlowFrame is amounts flowing between nodes, drawn as a Sankey diagram, or
// with Stages as an alluvial diagram. Nodes, when set, give the nodes' order;
// a node's value is the sum of its links.
type FlowFrame struct {
	Type   FrameType       `json:"type" wiregen:"const=flow"`
	Name   string          `json:"name"`
	Unit   string          `json:"unit"`
	Nodes  []FrameFlowNode `json:"nodes,omitempty"`
	Stages []string        `json:"stages,omitempty"`
	Links  []FrameFlowLink `json:"links"`
	Refs   []FrameRef      `json:"refs,omitempty"`
	Notes  []FrameNote     `json:"notes,omitempty"`
	// Read is how the read behind the frame went, absent when it was whole.
	Read *FrameRead `json:"read,omitempty"`
}

// FrameTimes is one row of an EventsFrame: its key and the instants of its
// events in Unix milliseconds, in order.
type FrameTimes struct {
	Key string  `json:"key"`
	At  []int64 `json:"at"`
}

// EventsFrame is rows of instants, drawn as a dot timeline strip.
type EventsFrame struct {
	Type  FrameType    `json:"type" wiregen:"const=events"`
	Name  string       `json:"name"`
	Rows  []FrameTimes `json:"rows"`
	Refs  []FrameRef   `json:"refs,omitempty"`
	Notes []FrameNote  `json:"notes,omitempty"`
	// Read is how the read behind the frame went, absent when it was whole.
	Read *FrameRead `json:"read,omitempty"`
}

// StatesFrame is a state per observation, oldest first, drawn as a state
// history: Keys and At are equally long, and a null key is a gap.
type StatesFrame struct {
	Type  FrameType   `json:"type" wiregen:"const=states"`
	Name  string      `json:"name"`
	Keys  []*string   `json:"keys"`
	At    []int64     `json:"at"`
	Refs  []FrameRef  `json:"refs,omitempty"`
	Notes []FrameNote `json:"notes,omitempty"`
	// Read is how the read behind the frame went, absent when it was whole.
	Read *FrameRead `json:"read,omitempty"`
}

// FrameMeter is one meter of a ValueFrame: a value between Min and Max, and
// the optional bounds of its low and high ranges and its best value.
type FrameMeter struct {
	Low     *float64 `json:"low,omitempty"`
	High    *float64 `json:"high,omitempty"`
	Optimum *float64 `json:"optimum,omitempty"`
	Key     string   `json:"key"`
	Value   float64  `json:"value"`
	Min     float64  `json:"min"`
	Max     float64  `json:"max"`
}

// ValueFrame is one meter, or several sharing one bar, such as one per
// language.
type ValueFrame struct {
	Type   FrameType    `json:"type" wiregen:"const=value"`
	Name   string       `json:"name"`
	Unit   string       `json:"unit"`
	Meters []FrameMeter `json:"meters"`
	Refs   []FrameRef   `json:"refs,omitempty"`
	Notes  []FrameNote  `json:"notes,omitempty"`
	// Read is how the read behind the frame went, absent when it was whole.
	Read *FrameRead `json:"read,omitempty"`
}

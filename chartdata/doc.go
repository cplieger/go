// Package chartdata turns a service's series into the data its charts draw:
// typed frames that @cplieger/ui's chart elements take as they are, and live
// events that change a frame already drawn.
//
// A frame is built or sampled. A constructor builds one from whatever the
// app holds, such as a store query, and takes the [measure] declaration it
// draws, so the frame's name and unit come from the declaration and cannot
// disagree with the /metrics page. A [Sampler] holds the recent history of
// registered series in bounded rings, for the series the app keeps nowhere
// else, and emits a [FrameEvent] for each step. Every constructor refuses a
// malformed frame, and one past the limits the chart elements draw, with a
// [RefusalError]. A [Count], and a frame through [WithRead], carry how the
// read behind them went. The package keeps no store and never downsamples.
package chartdata

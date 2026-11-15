# Non-goals

This page lists what chartdata leaves out on purpose and why, for a developer deciding whether it fits or planning a change. These are design decisions, so open an issue to argue for one before sending a pull request.

| Left out | Reason |
| --- | --- |
| A store of past values | Prometheus keeps the operator's history, and the app's own store keeps product history. A `Sampler`'s rings are bounded and start empty at each restart, for the series the app keeps nowhere else |
| Reading the app's store | The app's own handler runs its query and passes the result to the constructors. One query can build every frame a screen needs, and the app caches what it likes |
| A registry of frame sources | A frame is built by a constructor or read from a `Sampler`, and nothing else. There is no registered reader and no "current values" source |
| Downsampling | A frame holds every point its window holds, within the limits table. A page that needs fewer points asks for a shorter window or a longer step |
| Presentation | A frame holds data only. The mark, its arrangement, colours, words, a unit's noun, height and empty text are the page's |
| A refusal that depends on the presentation | A log axis's thinnest bin or a plot's size in pixels is known only to the chart element, which refuses it there |
| Alert rules and thresholds | They live beside the app in its rule files, where they are reviewed and tested with `promtool`. A threshold drawn on a chart is a ref the server computed |
| A way into the registry | chartdata reads a registry only through `measure`'s reading API, `GatherOf` and each declaration's metadata. It registers nothing and opens no seam into the sealed registry |
| Prometheus formats beyond text 0.0.4 | Exposition is `measure`'s, which serves text 0.0.4 only. chartdata's wire is JSON frames |
| Serving the frames | The app serves them from its own API, behind its own session, and sends events on its own stream. chartdata has no HTTP handler |

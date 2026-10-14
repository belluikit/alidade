---
title: Fixture
tags: [docs, graph]
---

# Fixture

Read the [design](docs/design/overview.md) and the [decision](docs/adr/001-shape.md#decision).
Within this file, [the top](#fixture) resolves and [symbols](#symbols) is duplicated, so it does not.
Code lives in [config.go](other/config.go) and [the synth package](synth/); [gone.go](other/gone.go)
and [a moved package](flow/) are no longer there, [docs](docs/) holds no code, and
[a route](docs/guide/) or [a site path](/reference/) is not a path here.
The manifest lives in `governance.pxf`; the schema in `api/v1/evaluations.proto`.

## Symbols

`synth.Report` is a type, `Render` a function, and `EvaluationsService.Create` an rpc.
`Config` names two types and matches nothing; `INFERRED` and `Missing` match nothing either.
A word like Report in prose is never a mention.

## Symbols

Duplicate heading, kept apart by a suffix.

```go
package main
```

```
no language
```

BenchmarkFrame on PR #14 (ADR 017 amendment): count-mode coverage over the
module at 100 and 200 iterations, at PR #14's head before the fix (3429c27),
and the files PR #14 changed (merge base with 5bbd4fc .. 3429c27).

    cd internal/tools && go test -run '^$' -bench '^BenchmarkFrame$' -benchtime {100,200}x \
      -count 1 -covermode=count -coverpkg=github.com/rajasatyajit/ternly/... -coverprofile=…

The gate measured +7.4% there while the PR changed nothing BenchmarkFrame
runs: a layout artefact. TestAttributionFramePR14 checks the method says so.

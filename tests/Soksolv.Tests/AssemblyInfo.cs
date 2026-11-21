using Xunit;

// Many tests toggle process-wide static switches (ClusterChecker.AStar/SkipCapped/CheckFast, feature masks) to compare
// against Go's exact behaviour; running test classes in parallel made them flaky (e.g. HubPull_NodeSlicesMatchGo).
[assembly: CollectionBehavior(DisableTestParallelization = true)]

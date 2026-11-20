using System.Diagnostics;
using Soksolv.Bench.Explore;
using Soksolv.Diagnostics;

Console.Write(MachineInfo.Header());
string dir = args.Length > 1 ? args[1] : Path.Combine(AppContext.BaseDirectory, "../../../../../testdata");
int maxStates = args.Length > 2 ? int.Parse(args[2]) : 300_000;
string mode = args.Length > 0 ? args[0] : "explore";
if (mode == "explore") ExploreRunner.Run(dir, maxStates);

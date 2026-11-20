namespace Soksolv.Tests;

static class TestPaths
{
    public static string Root { get; } = FindRoot();
    static string FindRoot()
    {
        var d = new DirectoryInfo(AppContext.BaseDirectory);
        while (d != null && !File.Exists(Path.Combine(d.FullName, "Soksolv.slnx"))) d = d.Parent;
        return d?.FullName ?? throw new DirectoryNotFoundException("repo root not found");
    }
    public static string[] Level(string name) => Soksolv.Model.XsbCollection.ReadFile(Path.Combine(Root, "testdata", name + ".xsb"))[0];
}

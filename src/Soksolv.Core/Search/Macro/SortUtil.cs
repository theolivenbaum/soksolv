namespace Soksolv.Search;

/// <summary>Allocation-free sorts (Native AOT allocates a comparison delegate per Array.Sort/Span.Sort call).</summary>
internal static class SortUtil
{
    public static void Insertion(Span<int> a)
    {
        for (int i = 1; i < a.Length; i++)
        {
            int x = a[i], j = i - 1;
            while (j >= 0 && a[j] > x) { a[j + 1] = a[j]; j--; }
            a[j + 1] = x;
        }
    }
    public static void Insertion(Span<ulong> a)
    {
        for (int i = 1; i < a.Length; i++)
        {
            ulong x = a[i]; int j = i - 1;
            while (j >= 0 && a[j] > x) { a[j + 1] = a[j]; j--; }
            a[j + 1] = x;
        }
    }
    public static void Insertion(List<int> l) => Insertion(System.Runtime.InteropServices.CollectionsMarshal.AsSpan(l));
}

package main

// towardStop (exp-277), when set, ends the toward lane's current search
// (checked every 50 ms) so it can restart toward a newer target.
var towardStop func() bool

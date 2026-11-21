package main

import "flag"

// exp-477 debug flags (default off): make the forward FESS mimic Festival in one respect each.
var FXDupSkip, FXFestOrder, FXCell3, FXNoOOP bool

func init() {
	flag.BoolVar(&FXDupSkip, "fx-dupskip", false, "exp-477: a move into an existing node costs no node count and no schedule turn (Festival set_best_move)")
	flag.BoolVar(&FXFestOrder, "fx-festorder", false, "exp-477: Festival's score order (packed, connectivity, rooms, OOP, hotspots) for moves and cells")
	flag.BoolVar(&FXNoOOP, "fx-nooop", false, "exp-477: out-of-plan feature is 0 everywhere (Festival's OOP zone is empty on 97-100% of its A forward nodes)")
	flag.BoolVar(&FXCell3, "fx-cell3", false, "exp-477: forward FESS cell without room connectivity (Festival: packed, connectivity, OOP)")
}

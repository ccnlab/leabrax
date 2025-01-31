// Copyright (c) 2020, The Emergent Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pcore

import (
	"fmt"
	"log"
	"strings"

	"github.com/ccnlab/leabrax/leabra"
	"github.com/chewxy/math32"
	"github.com/goki/ki/kit"
	"github.com/goki/mat32"
)

// MatrixParams has parameters for Dorsal Striatum Matrix computation
// These are the main Go / NoGo gating units in BG driving updating of PFC WM in PBWM
type MatrixParams struct {
	ThalLay   string  `desc:"name of VThal layer -- needed to get overall gating output action"`
	ThalThr   float32 `def:"0.25" desc:"threshold for thal max activation (in pool) to be gated -- typically .25 or so to accurately reflect PFC output gating -- may need to adjust based on actual behavior"`
	Deriv     bool    `def:"true" desc:"use the sigmoid derivative factor 2 * Act * (1-Act) for matrix (recv) activity in modulating learning -- otherwise just multiply by activation directly -- this is generally beneficial for learning to prevent weights from continuing to increase when activations are already strong (and vice-versa for decreases)"`
	BurstGain float32 `def:"1" desc:"multiplicative gain factor applied to positive (burst) dopamine signals in computing DALrn effect learning dopamine value based on raw DA that we receive (D2R reversal occurs *after* applying Burst based on sign of raw DA)"`
	DipGain   float32 `def:"1" desc:"multiplicative gain factor applied to positive (burst) dopamine signals in computing DALrn effect learning dopamine value based on raw DA that we receive (D2R reversal occurs *after* applying Burst based on sign of raw DA)"`
}

func (mp *MatrixParams) Defaults() {
	if mp.ThalLay == "" {
		mp.ThalLay = "VThal"
	}
	mp.ThalThr = 0.25
	mp.Deriv = true
	mp.BurstGain = 1
	mp.DipGain = 1
}

// LrnFactor returns multiplicative factor for level of msn activation.  If Deriv
// is 2 * act * (1-act) -- the factor of 2 compensates for otherwise reduction in
// learning from these factors.  Otherwise is just act.
func (mp *MatrixParams) LrnFactor(act float32) float32 {
	if !mp.Deriv {
		return act
	}
	return 2 * act * (1 - act)
}

// MatrixLayer represents the dorsal matrisome MSN's that are the main
// Go / NoGo gating units in BG.  D1R = Go, D2R = NoGo.
type MatrixLayer struct {
	Layer
	DaR    DaReceptors  `desc:"dominant type of dopamine receptor -- D1R for Go pathway, D2R for NoGo"`
	Matrix MatrixParams `view:"inline" desc:"matrix parameters"`
	DALrn  float32      `inactive:"+" desc:"effective learning dopamine value for this layer: reflects DaR and Gains"`
	ACh    float32      `inactive:"+" desc:"acetylcholine value from CIN cholinergic interneurons reflecting the absolute value of reward or CS predictions thereof -- used for resetting the trace of matrix learning"`
}

var KiT_MatrixLayer = kit.Types.AddType(&MatrixLayer{}, leabra.LayerProps)

// Defaults in param.Sheet format
// Sel: "MatrixLayer", Desc: "defaults",
// 	Params: params.Params{
// 		"Layer.Inhib.Pool.On":      "false",
// 		"Layer.Inhib.Layer.On":     "true",
// 		"Layer.Inhib.Layer.Gi":     "1.5",
// 		"Layer.Inhib.Layer.FB":     "0.0",
// 		"Layer.Inhib.Self.On":      "true",
// 		"Layer.Inhib.Self.Gi":      "0.3", // 0.6 in localist -- expt
// 		"Layer.Inhib.Self.Tau":     "3.0",
// 		"Layer.Inhib.ActAvg.Fixed": "true",
// 		"Layer.Inhib.ActAvg.Init":  "0.25",
// 		"Layer.Act.XX1.Gain":       "20", // more graded -- still works with 40 but less Rt distrib
// 		"Layer.Act.Dt.VmTau":       "3.3",
// 		"Layer.Act.Dt.GTau":        "3",
// 		"Layer.Act.Init.Decay":     "0",
// 	}}

func (ly *MatrixLayer) Defaults() {
	ly.Layer.Defaults()
	ly.Matrix.Defaults()

	// special inhib params
	ly.Inhib.Pool.On = false
	ly.Inhib.Layer.On = true
	ly.Inhib.Layer.Gi = 1.5
	ly.Inhib.Layer.FB = 0
	ly.Inhib.Self.On = true
	ly.Inhib.Self.Gi = 0.3 // 0.6 in localist one
	ly.Inhib.Self.Tau = 3.0
	ly.Inhib.ActAvg.Fixed = true
	ly.Inhib.ActAvg.Init = 0.25
	ly.Act.XX1.Gain = 20  // more graded -- still works with 40 but less Rt distrib
	ly.Act.Dt.VmTau = 3.3 // fastest
	ly.Act.Dt.GTau = 3
	ly.Act.Init.Decay = 0

	// important: user needs to adjust wt scale of some PFC inputs vs others:
	// drivers vs. modulators

	for _, pji := range ly.RcvPrjns {
		pj := pji.(leabra.LeabraPrjn).AsLeabra()
		if _, ok := pj.Send.(*GPLayer); ok { // From GPe TA or In
			pj.WtScale.Abs = 3
			pj.Learn.Learn = false
			pj.Learn.Norm.On = false
			pj.Learn.Momentum.On = false
			pj.Learn.WtSig.Gain = 1
			pj.WtInit.Mean = 0.9
			pj.WtInit.Var = 0
			pj.WtInit.Sym = false
			if strings.HasSuffix(pj.Send.Name(), "GPeIn") { // GPeInToMtx
				pj.WtScale.Abs = 0.3 // counterbalance for GPeTA to reduce oscillations
			} else if strings.HasSuffix(pj.Send.Name(), "GPeTA") { // GPeTAToMtx
				if strings.HasSuffix(ly.Nm, "MtxGo") {
					pj.WtScale.Abs = 0.8
				} else {
					pj.WtScale.Abs = 0.3 // GPeTAToMtxNo must be weaker to prevent oscillations, even with GPeIn offset
				}
			}
		}
	}

	ly.UpdateParams()
}

// AChLayer interface:

func (ly *MatrixLayer) GetACh() float32    { return ly.ACh }
func (ly *MatrixLayer) SetACh(ach float32) { ly.ACh = ach }

func (ly *MatrixLayer) ThalLayer() (*VThalLayer, error) {
	tly, err := ly.Network.LayerByNameTry(ly.Matrix.ThalLay)
	if err != nil {
		log.Printf("MatrixLayer %s ThalLayer: %v\n", ly.Name(), err)
		return nil, err
	}
	return tly.(*VThalLayer), nil
}

func (ly *MatrixLayer) InitActs() {
	ly.Layer.InitActs()
	ly.DA = 0
	ly.DALrn = 0
	ly.ACh = 0
}

// ActFmG computes rate-code activation from Ge, Gi, Gl conductances
// and updates learning running-average activations from that Act.
// Matrix extends to call DALrnFmDA and updates AlphaMax -> ActLrn
func (ly *MatrixLayer) ActFmG(ltime *leabra.Time) {
	ly.Layer.ActFmG(ltime)
	ly.DAActLrn(ltime)
}

// DAActLrn sets effective learning dopamine value from given raw DA value,
// applying Burst and Dip Gain factors, and then reversing sign for D2R.
// Also sets ActLrn based on whether corresponding VThal stripe fired
// above ThalThr -- flips sign of learning for stripe firing vs. not.
func (ly *MatrixLayer) DAActLrn(ltime *leabra.Time) {
	da := ly.DA
	if da > 0 {
		da *= ly.Matrix.BurstGain
	} else {
		da *= ly.Matrix.DipGain
	}
	if ly.DaR == D2R {
		da *= -1
	}
	ly.DALrn = da
	if ltime.Cycle < ly.AlphaMaxCyc {
		return
	}
	tly, err := ly.ThalLayer()
	if err != nil {
		return
	}
	for ni := range ly.Neurons {
		nrn := &ly.Neurons[ni]
		if nrn.IsOff() {
			continue
		}
		amax := ly.Matrix.LrnFactor(ly.AlphaMaxs[ni])
		tact := tly.AlphaMaxs[nrn.SubPool-1]
		if tact > ly.Matrix.ThalThr {
			nrn.ActLrn = amax
		} else {
			nrn.ActLrn = -amax
		}
	}
}

// UnitVarIdx returns the index of given variable within the Neuron,
// according to UnitVarNames() list (using a map to lookup index),
// or -1 and error message if not found.
func (ly *MatrixLayer) UnitVarIdx(varNm string) (int, error) {
	vidx, err := ly.Layer.UnitVarIdx(varNm)
	if err == nil {
		return vidx, err
	}
	if !(varNm == "DALrn" || varNm == "ACh") {
		return -1, fmt.Errorf("pcore.NeuronVars: variable named: %s not found", varNm)
	}
	nn := len(leabra.NeuronVars)
	// nn = DA
	if varNm == "DALrn" {
		return nn + 1, nil
	}
	return nn + 2, nil
}

// UnitVal1D returns value of given variable index on given unit, using 1-dimensional index.
// returns NaN on invalid index.
// This is the core unit var access method used by other methods,
// so it is the only one that needs to be updated for derived layer types.
func (ly *MatrixLayer) UnitVal1D(varIdx int, idx int) float32 {
	nn := len(leabra.NeuronVars)
	if varIdx < 0 || varIdx > nn+2 { // nn = DA, nn+1 = DALrn, nn+2 = ACh
		return math32.NaN()
	}
	if varIdx <= nn { //
		return ly.Layer.UnitVal1D(varIdx, idx)
	}
	if idx < 0 || idx >= len(ly.Neurons) {
		return math32.NaN()
	}
	if varIdx > nn+2 {
		return math32.NaN()
	}
	if varIdx == nn+1 { // DALrn
		return ly.DALrn
	}
	return ly.ACh
}

//////////////////////////////////////////////////////////////////////
//  MatrixPrjn

// MatrixTraceParams for for trace-based learning in the MatrixPrjn.
// A trace of synaptic co-activity is formed, and then modulated by dopamine
// whenever it occurs.  This bridges the temporal gap between gating activity
// and subsequent activity, and is based biologically on synaptic tags.
// Trace is reset at time of reward based on ACh level from CINs.
type MatrixTraceParams struct {
	CurTrlDA bool    `def:"true" desc:"if true, current trial DA dopamine can drive learning (i.e., synaptic co-activity trace is updated prior to DA-driven dWt), otherwise DA is applied to existing trace before trace is updated, meaning that at least one trial must separate gating activity and DA"`
	Decay    float32 `def:"2" min:"0" desc:"multiplier on CIN ACh level for decaying prior traces -- decay never exceeds 1.  larger values drive strong credit assignment for any US outcome."`
}

func (tp *MatrixTraceParams) Defaults() {
	tp.CurTrlDA = true
	tp.Decay = 2
}

//////////////////////////////////////////////////////////////////////////////////////
//  MatrixPrjn

// MatrixPrjn does dopamine-modulated, gated trace learning, for Matrix learning
// in PBWM context
type MatrixPrjn struct {
	leabra.Prjn
	Trace  MatrixTraceParams `view:"inline" desc:"special parameters for matrix trace learning"`
	TrSyns []TraceSyn        `desc:"trace synaptic state values, ordered by the sending layer units which owns them -- one-to-one with SConIdx array"`
}

var KiT_MatrixPrjn = kit.Types.AddType(&MatrixPrjn{}, leabra.PrjnProps)

func (pj *MatrixPrjn) Defaults() {
	pj.Prjn.Defaults()
	pj.Trace.Defaults()
	// no additional factors
	pj.Learn.WtSig.Gain = 1
	pj.Learn.Norm.On = false
	pj.Learn.Momentum.On = false
	pj.Learn.WtBal.On = false
}

func (pj *MatrixPrjn) Build() error {
	err := pj.Prjn.Build()
	pj.TrSyns = make([]TraceSyn, len(pj.SConIdx))
	return err
}

func (pj *MatrixPrjn) ClearTrace() {
	for si := range pj.TrSyns {
		sy := &pj.TrSyns[si]
		sy.NTr = 0
		sy.Tr = 0
	}
}

func (pj *MatrixPrjn) InitWts() {
	pj.Prjn.InitWts()
	pj.ClearTrace()
}

// DWt computes the weight change (learning) -- on sending projections.
func (pj *MatrixPrjn) DWt() {
	if !pj.Learn.Learn {
		return
	}
	slay := pj.Send.(leabra.LeabraLayer).AsLeabra()
	rlay := pj.Recv.(*MatrixLayer)

	da := rlay.DA
	daLrn := rlay.DALrn // includes d2 reversal etc

	ach := rlay.ACh
	achDk := mat32.Min(1, ach*pj.Trace.Decay)

	for si := range slay.Neurons {
		sn := &slay.Neurons[si]
		nc := int(pj.SConN[si])
		st := int(pj.SConIdxSt[si])
		syns := pj.Syns[st : st+nc]
		trsyns := pj.TrSyns[st : st+nc]
		scons := pj.SConIdx[st : st+nc]

		for ci := range syns {
			sy := &syns[ci]
			trsy := &trsyns[ci]
			ri := scons[ci]
			rn := &rlay.Neurons[ri]

			tr := trsy.Tr

			ntr := rn.ActLrn * sn.ActLrn
			dwt := float32(0)

			if pj.Trace.CurTrlDA {
				tr += ntr
			}

			if da != 0 {
				dwt = daLrn * tr
			}
			tr -= achDk * tr // decay trace that drove dwt

			if !pj.Trace.CurTrlDA {
				tr += ntr
			}
			trsy.Tr = tr
			trsy.NTr = ntr

			norm := float32(1)
			if pj.Learn.Norm.On {
				norm = pj.Learn.Norm.NormFmAbsDWt(&sy.Norm, math32.Abs(dwt))
			} else {
				sy.Norm = trsy.NTr // store in norm, moment!
				sy.Moment = trsy.Tr
			}
			if pj.Learn.Momentum.On {
				dwt = norm * pj.Learn.Momentum.MomentFmDWt(&sy.Moment, dwt)
			} else {
				dwt *= norm
			}
			sy.DWt += pj.Learn.Lrate * dwt
		}
		// aggregate max DWtNorm over sending synapses
		if pj.Learn.Norm.On {
			maxNorm := float32(0)
			for ci := range syns {
				sy := &syns[ci]
				if sy.Norm > maxNorm {
					maxNorm = sy.Norm
				}
			}
			for ci := range syns {
				sy := &syns[ci]
				sy.Norm = maxNorm
			}
		}
	}
}

///////////////////////////////////////////////////////////////////////////////
// SynVals

// SynVarIdx returns the index of given variable within the synapse,
// according to *this prjn's* SynVarNames() list (using a map to lookup index),
// or -1 and error message if not found.
func (pj *MatrixPrjn) SynVarIdx(varNm string) (int, error) {
	vidx, err := pj.Prjn.SynVarIdx(varNm)
	if err == nil {
		return vidx, err
	}
	nn := pj.Prjn.SynVarNum()
	switch varNm {
	case "NTr":
		return nn, nil
	case "Tr":
		return nn + 1, nil
	}
	return -1, fmt.Errorf("MatrixPrjn SynVarIdx: variable name: %v not valid", varNm)
}

// SynVal1D returns value of given variable index (from SynVarIdx) on given SynIdx.
// Returns NaN on invalid index.
// This is the core synapse var access method used by other methods,
// so it is the only one that needs to be updated for derived layer types.
func (pj *MatrixPrjn) SynVal1D(varIdx int, synIdx int) float32 {
	if varIdx < 0 || varIdx >= len(SynVarsAll) {
		return math32.NaN()
	}
	nn := pj.Prjn.SynVarNum()
	if varIdx < nn {
		return pj.Prjn.SynVal1D(varIdx, synIdx)
	}
	if synIdx < 0 || synIdx >= len(pj.TrSyns) {
		return math32.NaN()
	}
	varIdx -= nn
	sy := &pj.TrSyns[synIdx]
	return sy.VarByIndex(varIdx)
}

// SynVarNum returns the number of synapse-level variables
// for this prjn.  This is needed for extending indexes in derived types.
func (pj *MatrixPrjn) SynVarNum() int {
	return pj.Prjn.SynVarNum() + len(TraceSynVars)
}

// domain/eventsignals/eventsignalstest/taxonomy.go

// Package eventsignalstest provides test fixtures for the eventsignals
// package.
package eventsignalstest

import es "github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"

// Taxonomy returns the reference event taxonomy (the one the example config
// declares), as a fresh value on every call so callers may mutate it.
func Taxonomy() es.Taxonomy {
	referential := func() []es.TypeFlag { return []es.TypeFlag{es.FlagReferential} }
	updateFraming := func() []es.TypeFlag { return []es.TypeFlag{es.FlagReferential, es.FlagUpdateFraming} }
	return es.Taxonomy{
		EventTypes: []es.EventTypeDef{
			{Name: es.EventTypeTransaction, Description: "A roster move: signing, release, trade, waiver, claim, extension, contract or practice-squad change."},
			{Name: es.EventTypeInjury, Description: "A player's injury, injury-list move, return, activation or game status."},
			{Name: es.EventTypeDraft, Description: "A draft pick or draft-related news."},
			{Name: es.EventTypeGame, Description: "A game preview or result.", Flags: []es.TypeFlag{es.FlagTeamLevelNeedsOpponent}},
			{Name: es.EventTypePractice, Description: "Practice participation, tryouts or depth chart changes."},
			{Name: es.EventTypeQuote, Description: "Coverage built around what someone said.", Flags: referential()},
			{Name: es.EventTypeRumor, Description: "An unconfirmed report or speculation.", Flags: referential()},
			{Name: es.EventTypeConfirmation, Description: "Confirmation of an earlier report.", Flags: updateFraming(), MemberRole: "confirmation"},
			{Name: es.EventTypeCorrection, Description: "A correction or walk-back of an earlier report.", Flags: updateFraming(), MemberRole: "correction"},
			{Name: es.EventTypeAnalysis, Description: "Analysis, predictions or breakdowns rather than news.", Flags: referential(), MemberRole: "analysis"},
			{Name: es.EventTypeReaction, Description: "Reactions to an event rather than the event itself.", Flags: referential(), MemberRole: "reaction"},
		},
		Actions: []es.ActionDef{
			{Name: es.ActionPracticeSquad, EventType: es.EventTypeTransaction, BodyFallbackRank: 2},
			{Name: es.ActionDemote, EventType: es.EventTypeTransaction, BodyFallbackRank: 4},
			{Name: es.ActionSign, EventType: es.EventTypeTransaction, BodyFallbackRank: 10},
			{Name: es.ActionRelease, EventType: es.EventTypeTransaction, BodyFallbackRank: 8},
			{Name: es.ActionTrade, EventType: es.EventTypeTransaction, BodyFallbackRank: 9},
			{Name: es.ActionWaive, EventType: es.EventTypeTransaction, BodyFallbackRank: 6},
			{Name: es.ActionClaim, EventType: es.EventTypeTransaction, BodyFallbackRank: 7},
			{Name: es.ActionExtend, EventType: es.EventTypeTransaction},
			{Name: es.ActionContract, EventType: es.EventTypeTransaction},
			{Name: es.ActionInjuredReserve, EventType: es.EventTypeInjury, BodyFallbackRank: 1},
			{Name: es.ActionInjure, EventType: es.EventTypeInjury, BodyFallbackRank: 12},
			{Name: es.ActionReturn, EventType: es.EventTypeInjury},
			{Name: es.ActionPractice, EventType: es.EventTypePractice},
			{Name: es.ActionActivate, EventType: es.EventTypeInjury, BodyFallbackRank: 5},
			{Name: es.ActionPromote, EventType: es.EventTypeTransaction, BodyFallbackRank: 3},
			{Name: es.ActionRoster, EventType: es.EventTypeTransaction, BodyFallbackRank: 11},
			{Name: es.ActionDepthChart, EventType: es.EventTypePractice},
			{Name: es.ActionTryout, EventType: es.EventTypePractice},
			{Name: es.ActionGameStatus, EventType: es.EventTypeInjury},
			{Name: es.ActionDraft, EventType: es.EventTypeDraft},
			{Name: es.ActionPreview, EventType: es.EventTypeGame},
			{Name: es.ActionResult, EventType: es.EventTypeGame},
			{Name: es.ActionQuote, EventType: es.EventTypeQuote},
			{Name: es.ActionReport, EventType: es.EventTypeRumor},
			{Name: es.ActionConfirm, EventType: es.EventTypeConfirmation},
			{Name: es.ActionCorrect, EventType: es.EventTypeCorrection},
		},
		Signals: []es.SignalDef{
			{Name: es.ContentSignalCorrection, EventType: es.EventTypeCorrection, Precedence: es.PrecedenceBeforeAction},
			{Name: es.ContentSignalConfirmation, EventType: es.EventTypeConfirmation, Precedence: es.PrecedenceBeforeAction},
			{Name: es.ContentSignalRumor, EventType: es.EventTypeRumor, Precedence: es.PrecedenceBeforeAction, Flags: []es.SignalFlag{es.FlagSpeculative}},
			{Name: es.ContentSignalAnalysis, EventType: es.EventTypeAnalysis, Precedence: es.PrecedenceAfterAction},
			{Name: es.ContentSignalReaction, EventType: es.EventTypeReaction, Precedence: es.PrecedenceAfterAction},
			{Name: es.ContentSignalQuote, EventType: es.EventTypeQuote, Precedence: es.PrecedenceAfterAction},
		},
	}
}

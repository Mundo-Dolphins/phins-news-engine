-- migrations/0041_entity_proposals_decision_reason.sql
--
-- Records why an entity proposal left pending (e.g. auto_rejected_non_nfl
-- from NFL entity verification). Empty for manual decisions.
ALTER TABLE entity_proposals ADD COLUMN decision_reason TEXT NOT NULL DEFAULT '';

-- migrations/0043_proposal_verification.sql
--
-- Provenance of the verifier that decided/enriched an entity proposal. Kept separate from extractor_version.
ALTER TABLE entity_proposals ADD COLUMN verification_provider TEXT NOT NULL DEFAULT '';
ALTER TABLE entity_proposals ADD COLUMN verification_provider_id TEXT NOT NULL DEFAULT '';
ALTER TABLE entity_proposals ADD COLUMN verification_version TEXT NOT NULL DEFAULT '';

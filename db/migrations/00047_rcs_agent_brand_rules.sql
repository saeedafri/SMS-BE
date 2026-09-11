-- +goose Up

-- The carriers' own vocabulary, and the carriers' own name limit.
--
-- MULTI_USE was declared provisionally and no carrier recognises it: Airtel p7
-- enumerates exactly three use cases and Vi enumerates none, so the set below
-- is the carriers' and not ours. An agent drafted under MULTI_USE is refused at
-- submission — days later, in a review queue the customer cannot see — rather
-- than at the moment it was typed.
--
-- Dropped rather than deprecated. This is the SILENT direction of a contract
-- change on our side: an enum losing a member deletes a generated constant
-- nobody names, so the build stays green and nothing reports that the value is
-- gone. The check constraint is the only thing that would have noticed, and it
-- still admitted it.
--
-- Safe as a straight tightening: no row on any deployment holds MULTI_USE
-- (production carries four agents, all TRANSACTIONAL, checked 11 September
-- 2026). A row that did would fail this migration loudly, which is correct —
-- an agent no carrier will accept should not be migrated quietly forward.
ALTER TABLE rcs_agents DROP CONSTRAINT rcs_agents_use_case_known;
ALTER TABLE rcs_agents ADD CONSTRAINT rcs_agents_use_case_known CHECK (
    use_case IN ('OTP', 'TRANSACTIONAL', 'PROMOTIONAL'));

-- Airtel p6 caps the agent name at 40 characters, and the name is what the
-- handset draws. Enforced at the edge as well, with a message naming both
-- numbers; this is the floor under it, so a write that reaches the table by any
-- other path cannot store a name the carrier will refuse.
--
-- Characters, not bytes: char_length, because a name in Devanagari is well
-- inside 40 characters and well over 40 bytes, and octet_length here would be a
-- rule about our encoding rather than about the carrier's.
ALTER TABLE rcs_agents ADD CONSTRAINT rcs_agents_display_name_fits CHECK (
    char_length(display_name) BETWEEN 1 AND 40);

-- +goose Down
ALTER TABLE rcs_agents DROP CONSTRAINT rcs_agents_display_name_fits;
ALTER TABLE rcs_agents DROP CONSTRAINT rcs_agents_use_case_known;
ALTER TABLE rcs_agents ADD CONSTRAINT rcs_agents_use_case_known CHECK (
    use_case IN ('OTP', 'TRANSACTIONAL', 'PROMOTIONAL', 'MULTI_USE'));

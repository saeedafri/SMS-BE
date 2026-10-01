-- +goose Up

-- Keyword chatbots.
--
-- A customer replies "OFFER" and gets the offer, without a person in the inbox.
-- A flow names the keywords that start it, the sender it answers from, and the
-- reply: a registered template or plain text.
--
-- Keywords live in their own table so "one keyword, one flow, per channel" is a
-- primary key rather than a check somebody has to remember to run: two flows
-- both answering YES would send the customer two replies to one word.
CREATE TABLE chatbot_flows (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name        text        NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    channel     text        NOT NULL CHECK (channel IN ('SMS','RCS','WHATSAPP')),
    sender_id   uuid        NOT NULL,
    template_id uuid,
    reply_body  text        CHECK (reply_body IS NULL OR length(reply_body) <= 1600),
    active      boolean     NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chatbot_flows_has_reply CHECK (template_id IS NOT NULL OR reply_body IS NOT NULL)
);
CREATE INDEX chatbot_flows_tenant ON chatbot_flows (tenant_id, created_at DESC);

CREATE TABLE chatbot_keywords (
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    channel   text NOT NULL,
    keyword   text NOT NULL CHECK (keyword = lower(keyword) AND length(keyword) BETWEEN 1 AND 40),
    flow_id   uuid NOT NULL REFERENCES chatbot_flows(id) ON DELETE CASCADE,
    PRIMARY KEY (tenant_id, channel, keyword)
);
CREATE INDEX chatbot_keywords_flow ON chatbot_keywords (flow_id);

ALTER TABLE chatbot_flows ENABLE ROW LEVEL SECURITY;
ALTER TABLE chatbot_flows FORCE  ROW LEVEL SECURITY;
CREATE POLICY chatbot_flows_isolation ON chatbot_flows
    USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON chatbot_flows TO sms_app;

ALTER TABLE chatbot_keywords ENABLE ROW LEVEL SECURITY;
ALTER TABLE chatbot_keywords FORCE  ROW LEVEL SECURITY;
CREATE POLICY chatbot_keywords_isolation ON chatbot_keywords
    USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON chatbot_keywords TO sms_app;

-- +goose Down
DROP TABLE chatbot_keywords;
DROP TABLE chatbot_flows;

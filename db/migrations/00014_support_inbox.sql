-- +goose Up

CREATE TABLE support_tickets (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    subject    text NOT NULL,
    category   text NOT NULL CHECK (category IN ('billing','technical','compliance','other')),
    status     text NOT NULL DEFAULT 'open' CHECK (status IN ('open','pending','resolved')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX support_tickets_page ON support_tickets (tenant_id, updated_at DESC, id DESC);

CREATE TABLE support_messages (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    ticket_id   uuid NOT NULL REFERENCES support_tickets(id) ON DELETE CASCADE,
    author      text NOT NULL CHECK (author IN ('customer','operator')),
    author_name text NOT NULL,
    body        text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX support_messages_thread ON support_messages (ticket_id, created_at, id);

-- A conversation is a two-way thread with one contact on one channel.
CREATE TABLE conversations (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    contact_id uuid NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
    channel    text NOT NULL CHECK (channel IN ('SMS','RCS','WHATSAPP','EMAIL')),
    status     text NOT NULL DEFAULT 'open' CHECK (status IN ('open','closed')),
    unread     boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    -- One thread per contact per channel. Without this an inbound message
    -- could open a second conversation with the same person and the operator
    -- would answer in a thread the customer never sees.
    UNIQUE (tenant_id, contact_id, channel)
);

CREATE INDEX conversations_page ON conversations (tenant_id, updated_at DESC, id DESC);

CREATE TABLE conversation_messages (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    conversation_id uuid NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    direction       text NOT NULL CHECK (direction IN ('inbound','outbound')),
    body            text NOT NULL,
    -- Set only on an inbound message that matched a STOP-class keyword. Kept
    -- so an opt-out can be traced back to the exact words the person sent,
    -- which is the evidence a regulator asks for.
    keyword_matched text,
    segments        integer,
    status          text,
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX conversation_messages_thread
    ON conversation_messages (conversation_id, created_at, id);

ALTER TABLE support_tickets       ENABLE ROW LEVEL SECURITY;
ALTER TABLE support_tickets       FORCE  ROW LEVEL SECURITY;
ALTER TABLE support_messages      ENABLE ROW LEVEL SECURITY;
ALTER TABLE support_messages      FORCE  ROW LEVEL SECURITY;
ALTER TABLE conversations         ENABLE ROW LEVEL SECURITY;
ALTER TABLE conversations         FORCE  ROW LEVEL SECURITY;
ALTER TABLE conversation_messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE conversation_messages FORCE  ROW LEVEL SECURITY;

CREATE POLICY support_tickets_isolation ON support_tickets
    USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY support_messages_isolation ON support_messages
    USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY conversations_isolation ON conversations
    USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY conversation_messages_isolation ON conversation_messages
    USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON support_tickets TO sms_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON support_messages TO sms_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON conversations TO sms_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON conversation_messages TO sms_app;

-- +goose Down
DROP TABLE conversation_messages;
DROP TABLE conversations;
DROP TABLE support_messages;
DROP TABLE support_tickets;

# Contract delta: what our `openapi.json` has that the backend's build does not

Generated 2026-11-23 by `node scripts/contract-delta.cjs`. **Do not edit by hand and do not trust a copy older than the commits below: re-run it.**

- Our contract: frontend `e5613f1f` (212 paths, 372 schemas).
- Their build: backend `3f9ad83` (189 paths, 318 schemas), read from the spec compiled into `internal/gen/api/control.gen.go`.
- 174 real differences. 7 further differences are description text only and are not listed: regenerating from our file copies them.

Reading it: **only in ours** is something to build. **only in theirs** is something they built that our contract replaced or removed: delete or migrate it, because the contract is ours. **changed** is a value that differs. Enum, `required` and `oneOf` entries are compared as sets, parameters by name.

## WP1  (15)

### Only in ours: build this

- `/paths//v1/capabilities` {get}
- `/paths//v1/operator/channels` {get}
- `/paths//v1/operator/channels/{channel}/go-live-check` {post}
- `/paths//v1/operator/tenants/{id}/channels/{channel}` {patch}
- `/components/schemas/CapabilityMode` {type, enum, description}
- `/components/schemas/SetupStep` {type, required, properties}
- `/components/schemas/ChannelCapability` {type, required, properties}
- `/components/schemas/CapabilitiesResponse` {type, required, properties}
- `/components/schemas/OperatorChannel` {type, required, properties}
- `/components/schemas/OperatorChannelsResponse` {type, required, properties}
- `/components/schemas/GoLiveCheckStep` {type, required, properties}
- `/components/schemas/GoLiveCheckResult` {type, required, properties}
- `/components/schemas/SetTenantChannelRequest` {type, required, additionalProperties, properties}
- `/components/schemas/MessageRefusalCode/enum` member "channel_unavailable"
- `/components/schemas/TenantDetail/properties/channels` {type, items, description}

## WP2  (26)

### Only in ours: build this

- `/paths//v1/operator/failure-reasons` {get, post}
- `/paths//v1/operator/failure-reasons/unmapped` {get}
- `/paths//v1/operator/failure-reasons/{id}` {patch}
- `/paths//v1/messages/get/parameters/query:layer` {name, in, required, schema, description}
- `/components/schemas/WebhookEvent/properties/payload` {type, nullable, additionalProperties, description}
- `/components/schemas/FailureLayer` {type, enum, description}
- `/components/schemas/FailureSource` {type, enum, description}
- `/components/schemas/FailureFixAction` {type, required, properties}
- `/components/schemas/MessageFailure` {type, description, required, properties}
- `/components/schemas/MessageTimelineStep` {type, required, properties}
- `/components/schemas/FailureReasonMatch` {type, required, properties}
- `/components/schemas/FailureReason` {type, description, required, properties}
- `/components/schemas/FailureReasonPage` {type, required, properties}
- `/components/schemas/CreateFailureReasonRequest` {type, additionalProperties, required, description, properties}
- `/components/schemas/UpdateFailureReasonRequest` {type, additionalProperties, minProperties, description, properties}
- `/components/schemas/UnmappedFailureCode` {type, required, properties}
- `/components/schemas/UnmappedFailureCodePage` {type, required, properties}
- `/components/schemas/MessageLogEntry/properties/simulated` {type, description}
- `/components/schemas/MessageLogEntry/properties/failure` {allOf, nullable, description}
- `/components/schemas/MessageLogEntry/properties/timeline` {type, items, description}
- `/components/schemas/MessageLogEntry/properties/encoding` {type, enum, nullable, description}
- `/components/schemas/AuditAction/enum` member "failure_reason.create"
- `/components/schemas/AuditAction/enum` member "failure_reason.update"
- `/components/schemas/ErrorBreakdown/properties/errors/items/properties/layer` {allOf, nullable, description}
- `/components/schemas/ErrorBreakdown/properties/errors/items/properties/title` {type, description}
- `/components/schemas/ErrorBreakdown/properties/byLayer` {type, description, items}

## WP3  (31)

### Only in ours: build this

- `/paths//v1/sender-ids/{id}/verify-dns` {post}
- `/paths//v1/email/sending-health` {get}
- `/paths//v1/operator/tenants/{id}/email` {get}
- `/paths//v1/operator/tenants/{id}/email/pause` {post}
- `/paths//v1/operator/tenants/{id}/email/resume` {post}
- `/paths//v1/public/unsubscribe/{token}` {get, post}
- `/paths//v1/campaigns/post/requestBody/content/application/json/schema/properties/emailTracking` {type, required, properties, description}
- `/components/schemas/SenderId/properties/emailIdentity` {allOf, nullable, description}
- `/components/schemas/EmailDnsRecord/properties/type/enum` member "MAIL_FROM_MX"
- `/components/schemas/EmailDnsRecord/properties/type/enum` member "MAIL_FROM_SPF"
- `/components/schemas/EmailDnsRecord/properties/recordType` {type, enum, description}
- `/components/schemas/EmailDnsRecord/properties/priority` {type, nullable, description}
- `/components/schemas/EmailDnsRecord/properties/required` {type, description}
- `/components/schemas/EmailDnsRecord/properties/lastCheckedAt` {type, format, nullable, description}
- `/components/schemas/EmailContent/properties/bodyText` {type, nullable, description}
- `/components/schemas/SuppressionReason/enum` member "complaint"
- `/components/schemas/WebhookEventType/enum` member "message.bounced"
- `/components/schemas/WebhookEventType/enum` member "message.complained"
- `/components/schemas/WebhookEventType/enum` member "message.unsubscribed"
- `/components/schemas/Campaign/properties/emailTracking` {type, required, properties, description}
- `/components/schemas/EmailIdentityStatus` {type, enum, description}
- `/components/schemas/EmailIdentity` {type, required, properties}
- `/components/schemas/EmailEvent` {type, required, properties}
- `/components/schemas/EmailSendingStatus` {type, enum, description}
- `/components/schemas/EmailReputationFinding` {type, required, properties}
- `/components/schemas/EmailSendingHealth` {type, required, properties}
- `/components/schemas/OperatorTenantEmail` {type, required, properties}
- `/components/schemas/PublicUnsubscribeInfo` {type, required, properties}
- `/components/schemas/MessageLogEntry/properties/emailEvents` {type, items, description}
- `/components/schemas/AuditAction/enum` member "tenant.email_pause"
- `/components/schemas/AuditAction/enum` member "tenant.email_resume"

## WP4  (47)

### Only in ours: build this

- `/paths//v1/templates/post/requestBody/content/application/json/schema/properties/waDefinition` {allOf, nullable, description}
- `/paths//v1/whatsapp/embedded-signup/config` {get}
- `/paths//v1/whatsapp/embedded-signup/complete` {post}
- `/paths//v1/sender-ids/{id}/verify-setup` {post}
- `/paths//v1/operator/whatsapp/onboarding` {get}
- `/components/schemas/SenderId/properties/messagingLimit` {allOf, nullable, description}
- `/components/schemas/SenderId/properties/maxUniqueContacts24h` {type, nullable, description}
- `/components/schemas/SenderId/properties/tokenStatus` {type, enum, description}
- `/components/schemas/SenderId/properties/displayNameStatus` {type, enum}
- `/components/schemas/SenderId/properties/billing` {type, enum, description}
- `/components/schemas/SenderId/properties/paymentMethod` {type, enum}
- `/components/schemas/SenderId/properties/billingCurrency` {type, nullable}
- `/components/schemas/SenderId/properties/waSetup` {type, items, description}
- `/components/schemas/SenderId/properties/uniqueContactsUsed24h` {type, nullable, description}
- `/components/schemas/SenderId/properties/registrationAttemptsLeft` {type, nullable, description}
- `/components/schemas/SenderId/properties/restricted` {type, nullable, description}
- `/components/schemas/WaQualityRating/enum` member "unknown"
- `/components/schemas/WaMessagingLimit` {type, enum, description}
- `/components/schemas/Template/properties/waDefinition` {allOf, nullable, description}
- `/components/schemas/Template/properties/waState` {allOf, nullable, description}
- `/components/schemas/WebhookEventType/enum` member "sender.quality_changed"
- `/components/schemas/WebhookEventType/enum` member "sender.limit_changed"
- `/components/schemas/WebhookEventType/enum` member "sender.restricted"
- `/components/schemas/WebhookEventType/enum` member "sender.disconnected"
- `/components/schemas/WebhookEventType/enum` member "template.paused"
- `/components/schemas/WebhookEventType/enum` member "template.disabled"
- `/components/schemas/WebhookEventType/enum` member "template.category_changed"
- `/components/schemas/WaMetaCategory` {type, enum, description}
- `/components/schemas/WaTemplateStatus` {type, enum}
- `/components/schemas/WaRejectionReason` {type, enum}
- `/components/schemas/WaTemplateState` {type, required, properties}
- `/components/schemas/WaTemplateButton` {type, required, properties}
- `/components/schemas/WaTemplateComponent` {type, required, properties}
- `/components/schemas/WaTemplateDefinition` {type, required, properties}
- `/components/schemas/WaEmbeddedSignupConfig` {type, required, properties, description}
- `/components/schemas/WaEmbeddedSignupComplete` {type, additionalProperties, required, properties}
- `/components/schemas/OperatorWaOnboarding` {type, required, properties}
- `/components/schemas/MessageRefusalCode/enum` member "outside_customer_window"
- `/components/schemas/MessageRefusalCode/enum` member "onboarding_incomplete"
- `/components/schemas/MessageLogEntry/properties/providerMessageId` {type, nullable, description}
- `/components/schemas/MessageLogEntry/properties/providerBilling` {type, description, required, properties}
- `/components/schemas/Conversation/properties/windowExpiresAt` {type, format, nullable, description}
- `/components/schemas/NewConversationReplyRequest/properties/templateId` {type, format, description}
- `/components/schemas/NewConversationReplyRequest/properties/variables` {type, additionalProperties, description}

### Only in theirs: our contract replaced or removed it

- `/components/schemas/SenderId/properties/messagingTier` {nullable, oneOf}
- `/components/schemas/NewConversationReplyRequest/required` {body}
- `/components/schemas/WaMessagingTier` {description, enum, type}

## WP5  (34)

### Only in ours: build this

- `/paths//v1/operator/rcs-launches` {get}
- `/components/schemas/RcsContent/oneOf` member "#/components/schemas/RcsCarouselContent"
- `/components/schemas/RcsContent/discriminator/mapping/carousel` "#/components/schemas/RcsCarouselContent"
- `/components/schemas/RcsTextContent/properties/text/maxLength` 3072
- `/components/schemas/RcsTextContent/properties/suggestions/maxItems` 11
- `/components/schemas/RcsCardContent/properties/suggestions/maxItems` 11
- `/components/schemas/RcsCardContent/properties/orientation` {type, enum, description}
- `/components/schemas/RcsCardContent/properties/thumbnailAlignment` {type, enum, description}
- `/components/schemas/RcsCarouselContent` {type, required, properties}
- `/components/schemas/RcsCard/properties/mediaUrl/maxLength` 2048
- `/components/schemas/RcsCard/properties/title/maxLength` 200
- `/components/schemas/RcsCard/properties/description/maxLength` 2000
- `/components/schemas/RcsCard/properties/mediaHeight` {type, enum, description}
- `/components/schemas/RcsCard/properties/suggestions` {type, maxItems, items, description}
- `/components/schemas/RcsSuggestion/oneOf` member "#/components/schemas/RcsViewLocationSuggestion"
- `/components/schemas/RcsSuggestion/oneOf` member "#/components/schemas/RcsCreateCalendarEventSuggestion"
- `/components/schemas/RcsSuggestion/oneOf` member "#/components/schemas/RcsShareLocationSuggestion"
- `/components/schemas/RcsSuggestion/discriminator/mapping/view_location` "#/components/schemas/RcsViewLocationSuggestion"
- `/components/schemas/RcsSuggestion/discriminator/mapping/create_calendar_event` "#/components/schemas/RcsCreateCalendarEventSuggestion"
- `/components/schemas/RcsSuggestion/discriminator/mapping/share_location` "#/components/schemas/RcsShareLocationSuggestion"
- `/components/schemas/RcsReplySuggestion/properties/text/maxLength` 25
- `/components/schemas/RcsReplySuggestion/properties/postbackData` {type, maxLength, description}
- `/components/schemas/RcsOpenUrlSuggestion/properties/text/maxLength` 25
- `/components/schemas/RcsOpenUrlSuggestion/properties/url/maxLength` 2048
- `/components/schemas/RcsOpenUrlSuggestion/properties/postbackData` {type, maxLength, description}
- `/components/schemas/RcsOpenUrlSuggestion/properties/application` {type, enum, description}
- `/components/schemas/RcsOpenUrlSuggestion/properties/webviewViewMode` {type, enum, description}
- `/components/schemas/RcsDialSuggestion/properties/text/maxLength` 25
- `/components/schemas/RcsDialSuggestion/properties/postbackData` {type, maxLength, description}
- `/components/schemas/RcsViewLocationSuggestion` {type, required, properties}
- `/components/schemas/RcsCreateCalendarEventSuggestion` {type, required, properties}
- `/components/schemas/RcsShareLocationSuggestion` {type, required, properties}
- `/components/schemas/OperatorRcsCarrier` {type, description, required, properties}
- `/components/schemas/OperatorRcsLaunchesResponse` {type, required, properties}

## WP6  (21)

### Only in ours: build this

- `/paths//v1/campaigns/post/requestBody/content/application/json/schema/properties/quietHours` {allOf, nullable, description}
- `/paths//v1/links/domains` {get, post}
- `/paths//v1/links/domains/{id}` {get, delete}
- `/paths//v1/links/domains/{id}/validate` {post}
- `/paths//v1/lookups` {post}
- `/paths//v1/lookups/batch` {post}
- `/components/schemas/Contact/properties/lookup` {allOf, nullable, description}
- `/components/schemas/Campaign/properties/quietHours` {allOf, nullable, description}
- `/components/schemas/MessageRefusalCode/enum` member "url_not_whitelisted"
- `/components/schemas/MessageRefusalCode/enum` member "quiet_hours"
- `/components/schemas/ShortLinkCreate/properties/domainId` {type, format, description}
- `/components/schemas/ShortLinkBulkCreate/properties/domainId` {type, format, description}
- `/components/schemas/QuietHours` {type, required, description, properties}
- `/components/schemas/LinkDomainStatus` {type, enum, description}
- `/components/schemas/LinkDomain` {type, required, properties}
- `/components/schemas/LinkDomainPage` {type, required, properties}
- `/components/schemas/LinkDomainCreate` {type, required, properties}
- `/components/schemas/LookupResult` {type, required, properties}
- `/components/schemas/LookupRequest` {type, required, properties}
- `/components/schemas/LookupBatchRequest` {type, required, properties}
- `/components/schemas/LookupBatchResponse` {type, required, properties}

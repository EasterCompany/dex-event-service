package templates

import (
	"github.com/EasterCompany/dex-event-service/types"
)

// GetTemplates returns all available event templates
// All templates are hardcoded and versioned with the application
func GetTemplates() map[string]EventTemplate {
	return map[string]EventTemplate{
		"system.cognitive.model_load": {
			Description: "A cognitive model was loaded into memory",
			Format:      "Loaded model: {model} ({method})",
			Fields: map[string]FieldSpec{
				"model": {
					Type:        "string",
					Required:    true,
					Description: "Name of the model",
				},
				"method": {
					Type:        "string",
					Required:    true,
					Description: "Method used to load (generate, chat, etc.)",
				},
				"duration": {
					Type:        "string",
					Required:    false,
					Description: "Duration of the load operation",
				},
				"success": {
					Type:        "boolean",
					Required:    false,
					Description: "Whether the load was successful",
				},
				"error": {
					Type:        "string",
					Required:    false,
					Description: "Error message if failed",
				},
			},
		},

		"system.cognitive.model_unload": {
			Description: "A cognitive model was unloaded from memory",
			Format:      "Unloaded model: {model}",
			Fields: map[string]FieldSpec{
				"model": {
					Type:        "string",
					Required:    true,
					Description: "Name of the model",
				},
				"reason": {
					Type:        "string",
					Required:    false,
					Description: "Reason for unload (timeout, replacement, manual)",
				},
				"duration": {
					Type:        "string",
					Required:    false,
					Description: "Duration of the unload operation",
				},
				"success": {
					Type:        "boolean",
					Required:    false,
					Description: "Whether the unload was successful",
				},
			},
		},

		"message_received": {
			Description: "A message received from a chat platform (Discord, Slack, etc.)",
			Format:      "{user} posted in {channel}: {message}",
			Formats: map[string]string{
				// MVP Languages (v1.0.0 - Production Ready)
				"ru": "{user} опубликовал в {channel}: {message}",      // Russian
				"uk": "{user} опублікував у {channel}: {message}",      // Ukrainian
				"da": "{user} postede i {channel}: {message}",          // Danish
				"de": "{user} hat in {channel} gepostet: {message}",    // German
				"lt": "{user} paskelbė {channel}: {message}",           // Lithuanian
				"sr": "{user} je objavio u {channel}: {message}",       // Serbian
				"el": "{user} ανάρτησε στο {channel}: {message}",       // Greek
				"tr": "{user} {channel} kanalında paylaştı: {message}", // Turkish
				"ro": "{user} a postat în {channel}: {message}",        // Romanian

				// Future expansion placeholders (resolver + fallback already configured)
				// "fr": "{user} a posté dans {channel}: {message}",     // French
				// "es": "{user} publicó en {channel}: {message}",       // Spanish
				// "it": "{user} ha pubblicato in {channel}: {message}", // Italian
				// "pt": "{user} postou em {channel}: {message}",        // Portuguese
				// "nl": "{user} plaatste in {channel}: {message}",      // Dutch
				// "no": "{user} postet i {channel}: {message}",         // Norwegian
				// "sv": "{user} postade i {channel}: {message}",        // Swedish
				// "pl": "{user} opublikował w {channel}: {message}",    // Polish
				// "cs": "{user} zveřejnil v {channel}: {message}",      // Czech
				// "sk": "{user} uverejnil v {channel}: {message}",      // Slovak
				// "hr": "{user} je objavio u {channel}: {message}",     // Croatian
				// "bs": "{user} je objavio u {channel}: {message}",     // Bosnian
				// "sl": "{user} je objavil v {channel}: {message}",     // Slovenian
				// "mk": "{user} објави во {channel}: {message}",        // Macedonian
				// "bg": "{user} публикува в {channel}: {message}",      // Bulgarian
				// "lv": "{user} publicēja {channel}: {message}",        // Latvian
				// "et": "{user} postitas {channel}: {message}",         // Estonian
				// "fi": "{user} julkaisi kanavalla {channel}: {message}", // Finnish
				// "sq": "{user} postoi në {channel}: {message}",        // Albanian
				// "hu": "{user} közzétette a(z) {channel} csatornán: {message}", // Hungarian
			},
			Fields: map[string]FieldSpec{
				"user": {
					Type:        "string",
					Required:    true,
					Description: "Username or user ID who sent the message",
				},
				"user_id": {
					Type:        "string",
					Required:    false,
					Description: "Unique user identifier",
				},
				"message": {
					Type:        "string",
					Required:    true,
					Description: "The message content",
				},
				"channel": {
					Type:        "string",
					Required:    true,
					Description: "Channel name or ID where message was sent",
				},
				"channel_id": {
					Type:        "string",
					Required:    false,
					Description: "Unique channel identifier",
				},
				"server": {
					Type:        "string",
					Required:    false,
					Description: "Server/guild name",
				},
				"server_id": {
					Type:        "string",
					Required:    false,
					Description: "Unique server/guild identifier",
				},
				"attachments": {
					Type:        "array",
					Required:    false,
					Description: "List of attached files or media",
				},
				"reply_to": {
					Type:        "string",
					Required:    false,
					Description: "Message ID this is replying to",
				},
			},
		},

		"action_performed": {
			Description: "A user or system action was performed",
			Format:      "{actor} {action} {target}",
			Formats:     map[string]string{
				// All languages use same format (field values determine language)
			},
			Fields: map[string]FieldSpec{
				"actor": {
					Type:        "string",
					Required:    true,
					Description: "Who performed the action (user, system, service)",
				},
				"action": {
					Type:        "string",
					Required:    true,
					Description: "The action performed (created, updated, deleted, etc.)",
				},
				"target": {
					Type:        "string",
					Required:    true,
					Description: "What was acted upon (resource type/ID)",
				},
				"target_id": {
					Type:        "string",
					Required:    false,
					Description: "Unique identifier of the target",
				},
				"metadata": {
					Type:        "object",
					Required:    false,
					Description: "Additional context about the action",
				},
				"result": {
					Type:        "string",
					Required:    false,
					Description: "Outcome of the action (success, failure, partial)",
				},
			},
		},

		"log_entry": {
			Description: "General log entry for recording events with context",
			Format:      "[{level}] {message}",
			Formats:     map[string]string{
				// All languages use same format (brackets and structure are universal)
			},
			Fields: map[string]FieldSpec{
				"level": {
					Type:        "string",
					Required:    true,
					Description: "Log level (info, warning, error, debug)",
				},
				"message": {
					Type:        "string",
					Required:    true,
					Description: "Log message",
				},
				"context": {
					Type:        "object",
					Required:    false,
					Description: "Additional context data",
				},
				"source": {
					Type:        "string",
					Required:    false,
					Description: "Source component or function",
				},
			},
		},

		"error_occurred": {
			Description: "An error or exception occurred",
			Format:      "ERROR: {error}",
			Formats: map[string]string{
				// MVP Languages (v1.0.0 - Production Ready)
				"ru": "ОШИБКА: {error}",  // Russian
				"uk": "ПОМИЛКА: {error}", // Ukrainian
				"da": "FEJL: {error}",    // Danish
				"de": "FEHLER: {error}",  // German
				"lt": "KLAIDA: {error}",  // Lithuanian
				"sr": "GREŠKA: {error}",  // Serbian
				"el": "ΣΦΑΛΜΑ: {error}",  // Greek
				"tr": "HATA: {error}",    // Turkish
				"ro": "EROARE: {error}",  // Romanian

				// Future expansion placeholders
				// "fr": "ERREUR: {error}",     // French
				// "es": "ERROR: {error}",       // Spanish (same as English)
				// "it": "ERRORE: {error}",      // Italian
				// "pt": "ERRO: {error}",        // Portuguese
				// "nl": "FOUT: {error}",        // Dutch
				// "no": "FEIL: {error}",        // Norwegian
				// "sv": "FEL: {error}",         // Swedish
				// "pl": "BŁĄD: {error}",        // Polish
				// "cs": "CHYBA: {error}",       // Czech
				// "sk": "CHYBA: {error}",       // Slovak
				// "hr": "GREŠKA: {error}",      // Croatian
				// "bs": "GREŠKA: {error}",      // Bosnian
				// "sl": "NAPAKA: {error}",      // Slovenian
				// "mk": "ГРЕШКА: {error}",      // Macedonian
				// "bg": "ГРЕШКА: {error}",      // Bulgarian
				// "lv": "KĻŪDA: {error}",       // Latvian
				// "et": "VIGA: {error}",        // Estonian
				// "fi": "VIRHE: {error}",       // Finnish
				// "sq": "GABIM: {error}",       // Albanian
				// "hu": "HIBA: {error}",        // Hungarian
			},
			Fields: map[string]FieldSpec{
				"error": {
					Type:        "string",
					Required:    true,
					Description: "Error message",
				},
				"error_type": {
					Type:        "string",
					Required:    false,
					Description: "Type or category of error",
				},
				"stack_trace": {
					Type:        "string",
					Required:    false,
					Description: "Stack trace if available",
				},
				"context": {
					Type:        "object",
					Required:    false,
					Description: "Context when error occurred",
				},
				"severity": {
					Type:        "string",
					Required:    false,
					Description: "Error severity (low, medium, high, critical)",
				},
			},
		},

		"status_change": {
			Description: "A status or state change event",
			Format:      "{entity} changed status to {new_status}",
			Formats: map[string]string{
				// MVP Languages (v1.0.0 - Production Ready)
				"ru": "{entity} изменил статус на {new_status}",      // Russian
				"uk": "{entity} змінив статус на {new_status}",       // Ukrainian
				"da": "{entity} ændrede status til {new_status}",     // Danish
				"de": "{entity} hat Status geändert zu {new_status}", // German
				"lt": "{entity} pakeitė būseną į {new_status}",       // Lithuanian
				"sr": "{entity} je promenio status na {new_status}",  // Serbian
				"el": "{entity} άλλαξε κατάσταση σε {new_status}",    // Greek
				"tr": "{entity} durumu {new_status} olarak değişti",  // Turkish
				"ro": "{entity} a schimbat starea în {new_status}",   // Romanian

				// Future expansion placeholders
				// "fr": "{entity} a changé le statut en {new_status}",       // French
				// "es": "{entity} cambió el estado a {new_status}",           // Spanish
				// "it": "{entity} ha cambiato lo stato in {new_status}",     // Italian
				// "pt": "{entity} alterou o status para {new_status}",       // Portuguese
				// "nl": "{entity} heeft status gewijzigd naar {new_status}", // Dutch
				// "no": "{entity} endret status til {new_status}",            // Norwegian
				// "sv": "{entity} ändrade status till {new_status}",          // Swedish
				// "pl": "{entity} zmienił status na {new_status}",            // Polish
				// "cs": "{entity} změnil stav na {new_status}",               // Czech
				// "sk": "{entity} zmenil stav na {new_status}",               // Slovak
				// "hr": "{entity} je promijenio status u {new_status}",      // Croatian
				// "bs": "{entity} je promijenio status u {new_status}",      // Bosnian
				// "sl": "{entity} je spremenil status v {new_status}",       // Slovenian
				// "mk": "{entity} го промени статусот на {new_status}",      // Macedonian
				// "bg": "{entity} промени статуса на {new_status}",          // Bulgarian
				// "lv": "{entity} mainīja statusu uz {new_status}",          // Latvian
				// "et": "{entity} muutis olekut {new_status}",                // Estonian
				// "fi": "{entity} muutti tilan tilaksi {new_status}",        // Finnish
				// "sq": "{entity} ndryshoi statusin në {new_status}",        // Albanian
				// "hu": "{entity} állapotát {new_status}-ra változtatta",    // Hungarian
			},
			Fields: map[string]FieldSpec{
				"entity": {
					Type:        "string",
					Required:    true,
					Description: "What entity changed status",
				},
				"entity_id": {
					Type:        "string",
					Required:    false,
					Description: "Unique identifier of the entity",
				},
				"old_status": {
					Type:        "string",
					Required:    false,
					Description: "Previous status",
				},
				"new_status": {
					Type:        "string",
					Required:    true,
					Description: "New status",
				},
				"reason": {
					Type:        "string",
					Required:    false,
					Description: "Reason for the status change",
				},
				"metadata": {
					Type:        "object",
					Required:    false,
					Description: "Additional status change data",
				},
			},
		},

		"metric_recorded": {
			Description: "A metric or measurement was recorded",
			Format:      "{metric_name}: {value}{unit}",
			Formats:     map[string]string{
				// All languages use same format (metrics are universal)
			},
			Fields: map[string]FieldSpec{
				"metric_name": {
					Type:        "string",
					Required:    true,
					Description: "Name of the metric",
				},
				"value": {
					Type:        "number",
					Required:    true,
					Description: "Metric value",
				},
				"unit": {
					Type:        "string",
					Required:    false,
					Description: "Unit of measurement (ms, bytes, count, etc.)",
				},
				"tags": {
					Type:        "object",
					Required:    false,
					Description: "Tags or labels for the metric",
				},
			},
		},

		// NEW MESSAGING EVENTS START HERE

		string(types.EventTypeMessagingUserJoinedVoice): {
			Description: "A user joined a voice channel",
			Format:      "{user_name} joined voice channel {channel_name}",
			Fields: map[string]FieldSpec{
				"type":         {Type: "string", Required: true},
				"source":       {Type: "string", Required: true},
				"user_id":      {Type: "string", Required: true},
				"user_name":    {Type: "string", Required: true},
				"channel_id":   {Type: "string", Required: true},
				"channel_name": {Type: "string", Required: true},
				"server_id":    {Type: "string", Required: true},
				"server_name":  {Type: "string", Required: false},
				"timestamp":    {Type: "string", Required: true}, // time.Time marshals to string
			},
		},

		string(types.EventTypeMessagingUserLeftVoice): {
			Description: "A user left a voice channel",
			Format:      "{user_name} left voice channel {channel_name}",
			Fields: map[string]FieldSpec{
				"type":         {Type: "string", Required: true},
				"source":       {Type: "string", Required: true},
				"user_id":      {Type: "string", Required: true},
				"user_name":    {Type: "string", Required: true},
				"channel_id":   {Type: "string", Required: true},
				"channel_name": {Type: "string", Required: true},
				"server_id":    {Type: "string", Required: true},
				"server_name":  {Type: "string", Required: false},
				"timestamp":    {Type: "string", Required: true},
			},
		},

		string(types.EventTypeMessagingUserSentMessage): {
			Description: "A user sent a message in a text channel",
			Format:      "{user_name} in {channel_name}: {content}",
			Fields: map[string]FieldSpec{
				"type":         {Type: "string", Required: true},
				"source":       {Type: "string", Required: true},
				"user_id":      {Type: "string", Required: true},
				"user_name":    {Type: "string", Required: true},
				"channel_id":   {Type: "string", Required: true},
				"channel_name": {Type: "string", Required: true},
				"server_id":    {Type: "string", Required: true},
				"server_name":  {Type: "string", Required: false},
				"timestamp":    {Type: "string", Required: true},
				"message_id":   {Type: "string", Required: true},
				"content":      {Type: "string", Required: true},
				"mentioned_bot": {
					Type:        "boolean",
					Required:    false,
					Description: "Whether the bot was mentioned in the message",
				},
			},
		},

		string(types.EventTypeMessagingBotSentMessage): {
			Description: "The bot sent a message in a text channel",
			Format:      "Bot sent in {channel_name}: {content}",
			Fields: map[string]FieldSpec{
				"type":         {Type: "string", Required: true},
				"source":       {Type: "string", Required: true},
				"user_id":      {Type: "string", Required: true},
				"user_name":    {Type: "string", Required: true},
				"channel_id":   {Type: "string", Required: true},
				"channel_name": {Type: "string", Required: true},
				"server_id":    {Type: "string", Required: true},
				"server_name":  {Type: "string", Required: false},
				"timestamp":    {Type: "string", Required: true},
				"message_id":   {Type: "string", Required: true},
				"content":      {Type: "string", Required: true},
			},
		},

		"messaging.bot.joined_voice": {
			Description: "The bot joined a voice channel",
			Format:      "Dexter joined voice channel {channel_name}",
			Fields: map[string]FieldSpec{
				"channel_name": {Type: "string", Required: true},
				"channel_id":   {Type: "string", Required: true},
				"server_id":    {Type: "string", Required: true},
			},
		},

		string(types.EventTypeMessagingBotVoiceResponse): {
			Description: "The bot responded via voice",
			Format:      "Dexter said: {content}",
			Fields: map[string]FieldSpec{
				"user_name": {Type: "string", Required: true},
				"content":   {Type: "string", Required: true},
				"response_model": {
					Type:        "string",
					Required:    false,
					Description: "Model used for generating response",
				},
				"response_raw": {
					Type:        "string",
					Required:    false,
					Description: "Raw output from response model",
				},
				"raw_input": {
					Type:        "string",
					Required:    false,
					Description: "Raw input prompt to the model",
				},
			},
		},

		string(types.EventTypeMessagingBotStatusUpdate): {
			Description: "The bot's status has changed",
			Format:      "Bot status changed to {status}: {details}",
			Fields: map[string]FieldSpec{
				"type":      {Type: "string", Required: true},
				"source":    {Type: "string", Required: true},
				"status":    {Type: "string", Required: true},
				"details":   {Type: "string", Required: true},
				"timestamp": {Type: "string", Required: true},
			},
		},

		string(types.EventTypeMessagingUserSpeakingStarted): {
			Description: "A user started speaking",
			Format:      "{user_name} started speaking",
			Fields: map[string]FieldSpec{
				"type":         {Type: "string", Required: true},
				"source":       {Type: "string", Required: true},
				"user_id":      {Type: "string", Required: true},
				"user_name":    {Type: "string", Required: true},
				"channel_id":   {Type: "string", Required: true},
				"channel_name": {Type: "string", Required: true},
				"server_id":    {Type: "string", Required: true},
				"server_name":  {Type: "string", Required: false},
				"timestamp":    {Type: "string", Required: true},
				"ssrc":         {Type: "number", Required: true},
			},
		},

		string(types.EventTypeMessagingUserSpeakingStopped): {
			Description: "A user stopped speaking",
			Format:      "{user_name} stopped speaking",
			Fields: map[string]FieldSpec{
				"type":         {Type: "string", Required: true},
				"source":       {Type: "string", Required: true},
				"user_id":      {Type: "string", Required: true},
				"user_name":    {Type: "string", Required: true},
				"channel_id":   {Type: "string", Required: true},
				"channel_name": {Type: "string", Required: true},
				"server_id":    {Type: "string", Required: true},
				"server_name":  {Type: "string", Required: false},
				"timestamp":    {Type: "string", Required: true},
				"ssrc":         {Type: "number", Required: true},
			},
		},

		string(types.EventTypeMessagingUserTranscribed): {
			Description: "A user's speech was transcribed",
			Format:      "{user_name} said: {transcription}",
			Fields: map[string]FieldSpec{
				"type":          {Type: "string", Required: true},
				"source":        {Type: "string", Required: true},
				"user_id":       {Type: "string", Required: true},
				"user_name":     {Type: "string", Required: true},
				"channel_id":    {Type: "string", Required: true},
				"channel_name":  {Type: "string", Required: true},
				"server_id":     {Type: "string", Required: true},
				"server_name":   {Type: "string", Required: false},
				"timestamp":     {Type: "string", Required: true},
				"transcription": {Type: "string", Required: true},
				"detected_language": {
					Type:        "string",
					Required:    false,
					Description: "Language code detected by Whisper",
				},
				"english_translation": {
					Type:        "string",
					Required:    false,
					Description: "English translation if source was not English",
				},
			},
		},

		string(types.EventTypeMessagingUserJoinedServer): {
			Description: "A user joined the server",
			Format:      "{user_name} joined {server_name}",
			Fields: map[string]FieldSpec{
				"type":         {Type: "string", Required: true},
				"source":       {Type: "string", Required: true},
				"user_id":      {Type: "string", Required: true},
				"user_name":    {Type: "string", Required: true},
				"channel_id":   {Type: "string", Required: false}, // May not be applicable
				"channel_name": {Type: "string", Required: false},
				"server_id":    {Type: "string", Required: true},
				"server_name":  {Type: "string", Required: true},
				"timestamp":    {Type: "string", Required: true},
			},
		},

		string(types.EventTypeMessagingWebhookMessage): {
			Description: "A message received from a webhook",
			Format:      "{user_name} (Webhook) in {channel_name}: {content}",
			Fields: map[string]FieldSpec{
				"type":         {Type: "string", Required: true},
				"source":       {Type: "string", Required: true},
				"user_id":      {Type: "string", Required: true},
				"user_name":    {Type: "string", Required: true},
				"channel_id":   {Type: "string", Required: true},
				"channel_name": {Type: "string", Required: true},
				"server_id":    {Type: "string", Required: true},
				"server_name":  {Type: "string", Required: false},
				"timestamp":    {Type: "string", Required: true},
				"message_id":   {Type: "string", Required: true},
				"content":      {Type: "string", Required: true},
				"mentioned_bot": {
					Type:        "boolean",
					Required:    false,
					Description: "Whether the bot was mentioned in the message",
				},
			},
		},

		"webhook.processed": {
			Description: "A webhook event has been processed by the handler",
			Format:      "Webhook processed by {handler}: {status}",
			Fields: map[string]FieldSpec{
				"type":            {Type: "string", Required: true},
				"parent_event_id": {Type: "string", Required: true},
				"handler":         {Type: "string", Required: true},
				"status":          {Type: "string", Required: true},
			},
		},

		string(types.EventTypeModerationExplicitContentDeleted): {
			Description: "A message was deleted due to explicit content",
			Format:      "Explicit content deleted in {channel_name} from {user_name}: {reason}",
			Fields: map[string]FieldSpec{
				"type":         {Type: "string", Required: true},
				"source":       {Type: "string", Required: true},
				"user_id":      {Type: "string", Required: true},
				"user_name":    {Type: "string", Required: true},
				"channel_id":   {Type: "string", Required: true},
				"channel_name": {Type: "string", Required: true},
				"server_id":    {Type: "string", Required: true},
				"server_name":  {Type: "string", Required: false},
				"timestamp":    {Type: "string", Required: true},
				"message_id":   {Type: "string", Required: true},
				"reason":       {Type: "string", Required: true},
				"handler":      {Type: "string", Required: true},
				"raw_output":   {Type: "string", Required: false}, // Model output that triggered it
			},
		},

		string(types.EventTypeAnalysisVisualCompleted): {
			Description: "Visual analysis of an attachment is completed",
			Format:      "Analyzed image {filename}: {description}",
			Fields: map[string]FieldSpec{
				"type":            {Type: "string", Required: true},
				"parent_event_id": {Type: "string", Required: true},
				"handler":         {Type: "string", Required: true},
				"filename":        {Type: "string", Required: true},
				"description":     {Type: "string", Required: true},
				"timestamp":       {Type: "number", Required: true},
				"channel_id":      {Type: "string", Required: true},
				"user_id":         {Type: "string", Required: true},
				"server_id":       {Type: "string", Required: false},
			},
		},

		string(types.EventTypeAnalysisLinkCompleted): {
			Description: "Link analysis/unfurling is completed",
			Format:      "Analyzed link {url}: {title} - {description}\nSummary: {summary}",
			Fields: map[string]FieldSpec{
				"type":            {Type: "string", Required: true},
				"parent_event_id": {Type: "string", Required: true},
				"handler":         {Type: "string", Required: true},
				"url":             {Type: "string", Required: true},
				"title":           {Type: "string", Required: false},
				"description":     {Type: "string", Required: false},
				"summary":         {Type: "string", Required: false},
				"timestamp":       {Type: "number", Required: true},
				"channel_id":      {Type: "string", Required: true},
				"user_id":         {Type: "string", Required: true},
				"server_id":       {Type: "string", Required: false},
			},
		},

		string(types.EventTypeAnalysisRouterDecision): {
			Description: "The router model made a decision on how to process a link",
			Format:      "Router decision for {url}: {decision}",
			Fields: map[string]FieldSpec{
				"type":            {Type: "string", Required: true},
				"parent_event_id": {Type: "string", Required: true},
				"handler":         {Type: "string", Required: true},
				"url":             {Type: "string", Required: true},
				"decision":        {Type: "string", Required: true},
				"raw_output":      {Type: "string", Required: true},
				"raw_input":       {Type: "string", Required: true},
				"model":           {Type: "string", Required: true},
				"timestamp":       {Type: "number", Required: true},
				"channel_id":      {Type: "string", Required: true},
				"user_id":         {Type: "string", Required: true},
			},
		},

		string(types.EventTypeCLICommand): {
			Description: "A CLI command was executed",
			Format:      "CLI Command: {command} {args} ({status})",
			Fields: map[string]FieldSpec{
				"command":   {Type: "string", Required: true},
				"args":      {Type: "string", Required: false},
				"output":    {Type: "string", Required: false},
				"status":    {Type: "string", Required: true},
				"duration":  {Type: "string", Required: false},
				"exit_code": {Type: "number", Required: false},
			},
		},

		string(types.EventTypeCLIStatus): {
			Description: "CLI status update",
			Format:      "CLI Status: {status} - {message}",
			Fields: map[string]FieldSpec{
				"status":  {Type: "string", Required: true},
				"message": {Type: "string", Required: true},
			},
		},

		string(types.EventTypeSystemNotificationGenerated): {
			Description: "An AI-generated system notification",
			Format:      "Notification ({priority}): {title} - {body}",
			Fields: map[string]FieldSpec{
				"title":             {Type: "string", Required: true, Description: "Concise summary of the notification"},
				"priority":          {Type: "string", Required: true, Description: "Severity (low, medium, high, critical)"},
				"category":          {Type: "string", Required: true, Description: "Classification (system, security, conversation, error, build)"},
				"body":              {Type: "string", Required: true, Description: "Detailed explanation or suggested action"},
				"related_event_ids": {Type: "array", Required: false, Description: "IDs of related events"},
				"read":              {Type: "boolean", Required: false, Description: "Whether the user has marked it as read"},
				"alert":             {Type: "boolean", Required: false, Description: "Whether this is an urgent alert"},
			},
		},

		string(types.EventTypeSystemBlueprintGenerated): {
			Description: "An AI-generated technical blueprint for a new feature or optimization",
			Format:      "Blueprint: {title} - {summary}",
			Fields: map[string]FieldSpec{
				"title":               {Type: "string", Required: true},
				"priority":            {Type: "string", Required: true},
				"category":            {Type: "string", Required: true},
				"body":                {Type: "string", Required: true},
				"summary":             {Type: "string", Required: true},
				"content":             {Type: "string", Required: true},
				"affected_services":   {Type: "array", Required: true},
				"implementation_path": {Type: "array", Required: true},
				"read":                {Type: "boolean", Required: false},
			},
		},

		string(types.EventTypeSystemAnalysisAudit): {

			Description: "Emitted when a Guardian Tier completes an audit of system state.",

			Format: "Guardian Audit: {tier} ({model}) - {duration} [{status}]",

			Fields: map[string]FieldSpec{

				"agent_name": {Type: "string", Required: true},

				"tier": {Type: "string", Required: true, Description: "t1 or t2"},

				"model": {Type: "string", Required: true, Description: "Name of the model used"},

				"duration": {Type: "string", Required: true},

				"success": {Type: "boolean", Required: true},

				"status": {Type: "string", Required: true},

				"attempts": {Type: "number", Required: true},
			},
		},

		string(types.EventTypeSystemTestCompleted): {
			Description: "A service test suite has completed",
			Format:      "Tests completed for {service_name} ({duration})",
			Fields: map[string]FieldSpec{
				"service_name": {Type: "string", Required: true},
				"format":       {Type: "object", Required: true},
				"lint":         {Type: "object", Required: true},
				"test":         {Type: "object", Required: true},
				"duration":     {Type: "string", Required: true},
			},
		},

		string(types.EventTypeSystemBuildCompleted): {
			Description: "A service build has completed",
			Format:      "Build completed for {service_name}: {status}",
			Fields: map[string]FieldSpec{
				"service_name": {Type: "string", Required: true},
				"version":      {Type: "string", Required: true},
				"duration":     {Type: "string", Required: true},
				"status":       {Type: "string", Required: true},
			},
		},

		string(types.EventTypeSystemStatusChange): {
			Description: "A system-wide status or state change",
			Format:      "{entity} changed status to {new_status}",
			Fields: map[string]FieldSpec{
				"entity":     {Type: "string", Required: true},
				"new_status": {Type: "string", Required: true},
				"old_status": {Type: "string", Required: false},
				"reason":     {Type: "string", Required: false},
			},
		},

		// END NEW MESSAGING EVENTS

		"voice_speaking_started": {
			Description: "A user started speaking in a voice channel",
			Format:      "User {user_id} started speaking in voice channel {channel_id}",
			Formats:     map[string]string{
				// All languages use same format (user/channel IDs are universal)
			},
			Fields: map[string]FieldSpec{
				"user_id": {
					Type:        "string",
					Required:    true,
					Description: "Discord user ID who started speaking",
				},
				"channel_id": {
					Type:        "string",
					Required:    true,
					Description: "Discord voice channel ID",
				},
			},
		},

		"voice_speaking_stopped": {
			Description: "A user stopped speaking in a voice channel",
			Format:      "User {user_id} stopped speaking in voice channel {channel_id}",
			Formats:     map[string]string{
				// All languages use same format (user/channel IDs are universal)
			},
			Fields: map[string]FieldSpec{
				"user_id": {
					Type:        "string",
					Required:    true,
					Description: "Discord user ID who stopped speaking",
				},
				"channel_id": {
					Type:        "string",
					Required:    true,
					Description: "Discord voice channel ID",
				},
			},
		},

		"voice_transcribed": {
			Description: "Voice audio was transcribed to text",
			Format:      "User {user_id} said in voice channel {channel_id}: {transcription}",
			Formats:     map[string]string{
				// All languages use same format
			},
			Fields: map[string]FieldSpec{
				"user_id": {
					Type:        "string",
					Required:    true,
					Description: "Discord user ID who spoke",
				},
				"channel_id": {
					Type:        "string",
					Required:    true,
					Description: "Discord voice channel ID",
				},
				"transcription": {
					Type:        "string",
					Required:    true,
					Description: "Transcribed text from the voice audio",
				},
			},
		},

		"engagement.decision": {
			Description: "The system decided whether to engage with a user",
			Format:      "Engagement decision: {decision} (Reason: {reason})",
			Fields: map[string]FieldSpec{
				"decision": {
					Type:        "string",
					Required:    true,
					Description: "The decision made (engage/ignore/defer)",
				},
				"reason": {
					Type:        "string",
					Required:    false,
					Description: "The reason for the decision",
				},
				"context": {
					Type:        "string",
					Required:    false,
					Description: "Context used for the decision",
				},
				"handler": {
					Type:        "string",
					Required:    true,
					Description: "The handler that made the decision",
				},
				"event_id": {
					Type:        "string",
					Required:    true,
					Description: "ID of the triggering event",
				},
				"channel_id": {
					Type:        "string",
					Required:    false,
					Description: "Channel ID where the trigger occurred",
				},
				"user_id": {
					Type:        "string",
					Required:    false,
					Description: "User ID who triggered the event",
				},
				"message_content": {
					Type:        "string",
					Required:    false,
					Description: "Content of the triggering message",
				},
				"timestamp": {
					Type:        "number",
					Required:    true,
					Description: "Timestamp of the decision",
				},
				"engagement_model": {
					Type:        "string",
					Required:    false,
					Description: "Model used for engagement decision",
				},
				"response_model": {
					Type:        "string",
					Required:    false,
					Description: "Model used for generating response",
				},
				"input_prompt": {
					Type:        "string",
					Required:    false,
					Description: "Raw input prompt to the engagement model",
				},
				"context_history": {
					Type:        "string",
					Required:    false,
					Description: "Context history used in prompt",
				},
				"engagement_raw": {
					Type:        "string",
					Required:    false,
					Description: "Raw output from engagement model",
				},
				"response_raw": {
					Type:        "string",
					Required:    false,
					Description: "Raw output from response model",
				},
			},
		},

		"bot_response": {
			Description: "A response generated by the bot",
			Format:      "Bot responded: {response}",
			Fields: map[string]FieldSpec{
				"response": {
					Type:        "string",
					Required:    true,
					Description: "The response text",
				},
				"target_user": {
					Type:        "string",
					Required:    false,
					Description: "User ID the bot is responding to",
				},
				"target_channel": {
					Type:        "string",
					Required:    false,
					Description: "Channel ID where the response should be sent",
				},
			},
		},

		"system.diagnostic.ping": {
			Description: "A synthetic diagnostic ping to verify system health",
			Format:      "Diagnostic Ping: {ping_id} from {source}",
			Fields: map[string]FieldSpec{
				"ping_id": {
					Type:        "string",
					Required:    true,
					Description: "Unique identifier for this ping",
				},
				"source": {
					Type:        "string",
					Required:    true,
					Description: "Source service or component generating the ping",
				},
			},
		},
	}
}

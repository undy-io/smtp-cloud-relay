{{- define "smtp-cloud-relay.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "smtp-cloud-relay.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "smtp-cloud-relay.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "smtp-cloud-relay.labels" -}}
helm.sh/chart: {{ include "smtp-cloud-relay.chart" . }}
app.kubernetes.io/name: {{ include "smtp-cloud-relay.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "smtp-cloud-relay.selectorLabels" -}}
app.kubernetes.io/name: {{ include "smtp-cloud-relay.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "smtp-cloud-relay.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "smtp-cloud-relay.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "smtp-cloud-relay.secretName" -}}
{{- if .Values.secrets.create -}}
{{- printf "%s-secrets" (include "smtp-cloud-relay.fullname" .) -}}
{{- else -}}
{{- required "secrets.existingSecret is required when secrets.create=false" .Values.secrets.existingSecret -}}
{{- end -}}
{{- end -}}

{{- define "smtp-cloud-relay.spoolPvcName" -}}
{{- if .Values.spool.persistence.existingClaim -}}
{{- .Values.spool.persistence.existingClaim -}}
{{- else -}}
{{- printf "%s-spool" (include "smtp-cloud-relay.fullname" .) -}}
{{- end -}}
{{- end -}}

{{- define "smtp-cloud-relay.tlsSecretName" -}}
{{- $mode := lower (default "certManager" .Values.tls.mode) -}}
{{- if eq $mode "certmanager" -}}
{{- printf "%s-tls" (include "smtp-cloud-relay.fullname" .) -}}
{{- else if eq $mode "existingsecret" -}}
{{- required "tls.existingSecretName is required when tls.mode=existingSecret" .Values.tls.existingSecretName -}}
{{- end -}}
{{- end -}}

{{- define "smtp-cloud-relay.hostPort" -}}
{{- $host := trim (required "host is required" .host) -}}
{{- $port := int .port -}}
{{- if and (contains ":" $host) (not (hasPrefix "[" $host)) (not (hasSuffix "]" $host)) -}}
{{- printf "[%s]:%d" $host $port -}}
{{- else -}}
{{- printf "%s:%d" $host $port -}}
{{- end -}}
{{- end -}}

{{- define "smtp-cloud-relay.smtpListenAddr" -}}
{{- include "smtp-cloud-relay.hostPort" (dict "host" .Values.smtp.listenHost "port" .Values.smtp.port) -}}
{{- end -}}

{{- define "smtp-cloud-relay.smtpsListenAddr" -}}
{{- if .Values.smtp.smtps.enabled -}}
{{- include "smtp-cloud-relay.hostPort" (dict "host" .Values.smtp.smtps.listenHost "port" .Values.smtp.smtps.port) -}}
{{- end -}}
{{- end -}}

{{- define "smtp-cloud-relay.httpListenAddr" -}}
{{- include "smtp-cloud-relay.hostPort" (dict "host" .Values.http.listenHost "port" .Values.http.port) -}}
{{- end -}}

{{- define "smtp-cloud-relay.validateURLPortAllowed" -}}
{{- $raw := trim (default "" .url) -}}
{{- if $raw -}}
{{- $parsed := urlParse $raw -}}
{{- $host := trim (default "" $parsed.host) -}}
{{- if not $host -}}
{{- fail (printf "%s must be an absolute URL with host when set" .label) -}}
{{- end -}}
{{- $port := 0 -}}
{{- if regexMatch `:[0-9]+$` $host -}}
{{- $port = int (regexFind `[0-9]+$` $host) -}}
{{- else -}}
{{- $scheme := lower (default "" $parsed.scheme) -}}
{{- if eq $scheme "http" -}}
{{- $port = 80 -}}
{{- else if eq $scheme "https" -}}
{{- $port = 443 -}}
{{- end -}}
{{- end -}}
{{- if gt $port 0 -}}
{{- $allowed := false -}}
{{- range $candidate := .allowedPorts -}}
{{- if eq (int $candidate) $port -}}
{{- $allowed = true -}}
{{- end -}}
{{- end -}}
{{- if not $allowed -}}
{{- fail (printf "%s uses TCP port %d, which is not allowed by networkPolicy.egressTCPPorts" .label $port) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "smtp-cloud-relay.validateValues" -}}
{{- $mode := lower (default "acs" .Values.deliveryMode) -}}
{{- $tlsMode := lower (default "certManager" .Values.tls.mode) -}}
{{- $starttlsEnabled := true -}}
{{- if hasKey .Values.smtp "starttlsEnabled" -}}
{{- $starttlsEnabled = .Values.smtp.starttlsEnabled -}}
{{- end -}}
{{- $smtpsEnabled := false -}}
{{- if and (hasKey .Values.smtp "smtps") (hasKey .Values.smtp.smtps "enabled") -}}
{{- $smtpsEnabled = .Values.smtp.smtps.enabled -}}
{{- end -}}
{{- $tlsRequested := or $starttlsEnabled $smtpsEnabled .Values.smtp.requireTLS -}}
{{- $senderPolicyMode := lower (default "rewrite" .Values.senderPolicy.mode) -}}
{{- $egressPorts := list 80 443 -}}
{{- if hasKey .Values.networkPolicy "egressTCPPorts" -}}
{{- $egressPorts = .Values.networkPolicy.egressTCPPorts -}}
{{- end -}}
{{- if not (has $mode (list "acs" "ses" "noop")) -}}
{{- fail (printf "deliveryMode must be one of: acs, ses, noop (got %q)" .Values.deliveryMode) -}}
{{- end -}}
{{- if not (has $tlsMode (list "certmanager" "existingsecret" "disabled")) -}}
{{- fail (printf "tls.mode must be one of: certManager, existingSecret, disabled (got %q)" .Values.tls.mode) -}}
{{- end -}}
{{- if not (trim .Values.smtp.listenHost) -}}
{{- fail "smtp.listenHost must be non-empty" -}}
{{- end -}}
{{- if or (lt (int .Values.smtp.port) 1) (gt (int .Values.smtp.port) 65535) -}}
{{- fail "smtp.port must be between 1 and 65535" -}}
{{- end -}}
{{- if $smtpsEnabled -}}
{{- if not (trim .Values.smtp.smtps.listenHost) -}}
{{- fail "smtp.smtps.listenHost must be non-empty when smtp.smtps.enabled=true" -}}
{{- end -}}
{{- if or (lt (int .Values.smtp.smtps.port) 1) (gt (int .Values.smtp.smtps.port) 65535) -}}
{{- fail "smtp.smtps.port must be between 1 and 65535 when smtp.smtps.enabled=true" -}}
{{- end -}}
{{- end -}}
{{- if not (trim .Values.http.listenHost) -}}
{{- fail "http.listenHost must be non-empty" -}}
{{- end -}}
{{- if or (lt (int .Values.http.port) 1) (gt (int .Values.http.port) 65535) -}}
{{- fail "http.port must be between 1 and 65535" -}}
{{- end -}}
{{- if not .Values.smtp.allowedCIDRs -}}
{{- fail "smtp.allowedCIDRs must contain at least one entry" -}}
{{- end -}}
{{- range $i, $cidr := .Values.smtp.allowedCIDRs -}}
{{- if not (trim $cidr) -}}
{{- fail (printf "smtp.allowedCIDRs[%d] must be non-empty" $i) -}}
{{- end -}}
{{- end -}}
{{- if not .Values.smtp.requireAuth -}}
{{- fail "smtp.requireAuth must be true to avoid open relay behavior" -}}
{{- end -}}
{{- if ne (lower (default "static" .Values.smtp.authProvider)) "static" -}}
{{- fail (printf "smtp.authProvider must be static (got %q)" .Values.smtp.authProvider) -}}
{{- end -}}
{{- if not (has $senderPolicyMode (list "rewrite" "strict")) -}}
{{- fail (printf "senderPolicy.mode must be one of: rewrite, strict (got %q)" .Values.senderPolicy.mode) -}}
{{- end -}}
{{- if and .Values.smtp.requireTLS (not $starttlsEnabled) (not $smtpsEnabled) -}}
{{- fail "smtp.requireTLS=true requires smtp.starttlsEnabled=true or smtp.smtps.enabled=true" -}}
{{- end -}}
{{- if lt (int .Values.smtp.maxInflightSends) 1 -}}
{{- fail "smtp.maxInflightSends must be >= 1" -}}
{{- end -}}
{{- if lt (int .Values.delivery.retryAttempts) 1 -}}
{{- fail "delivery.retryAttempts must be >= 1" -}}
{{- end -}}
{{- if gt (int .Values.delivery.retryAttempts) 10 -}}
{{- fail "delivery.retryAttempts must be <= 10" -}}
{{- end -}}
{{- if lt (int .Values.delivery.retryBaseDelayMS) 1 -}}
{{- fail "delivery.retryBaseDelayMS must be >= 1" -}}
{{- end -}}
{{- if lt (int .Values.delivery.httpTimeoutMS) 1 -}}
{{- fail "delivery.httpTimeoutMS must be >= 1" -}}
{{- end -}}
{{- if lt (int .Values.delivery.httpMaxIdleConns) 1 -}}
{{- fail "delivery.httpMaxIdleConns must be >= 1" -}}
{{- end -}}
{{- if lt (int .Values.delivery.httpMaxIdleConnsPerHost) 1 -}}
{{- fail "delivery.httpMaxIdleConnsPerHost must be >= 1" -}}
{{- end -}}
{{- if lt (int .Values.delivery.httpIdleConnTimeoutMS) 1 -}}
{{- fail "delivery.httpIdleConnTimeoutMS must be >= 1" -}}
{{- end -}}
{{- if lt (int .Values.spool.pollIntervalMS) 1 -}}
{{- fail "spool.pollIntervalMS must be >= 1" -}}
{{- end -}}
{{- if and $tlsRequested (not (trim .Values.tls.certFile)) -}}
{{- fail "tls.certFile must be non-empty when SMTP TLS is enabled" -}}
{{- end -}}
{{- if and $tlsRequested (not (trim .Values.tls.keyFile)) -}}
{{- fail "tls.keyFile must be non-empty when SMTP TLS is enabled" -}}
{{- end -}}
{{- if eq $tlsMode "disabled" -}}
{{- if $tlsRequested -}}
{{- fail "tls.mode=disabled is only valid when smtp.starttlsEnabled=false, smtp.smtps.enabled=false, and smtp.requireTLS=false" -}}
{{- end -}}
{{- else if not $tlsRequested -}}
{{- fail "tls.mode must be disabled when smtp.starttlsEnabled=false, smtp.smtps.enabled=false, and smtp.requireTLS=false" -}}
{{- else if eq $tlsMode "existingsecret" -}}
{{- if and $tlsRequested (not (trim .Values.tls.existingSecretName)) -}}
{{- fail "tls.existingSecretName is required when tls.mode=existingSecret and SMTP TLS is enabled" -}}
{{- end -}}
{{- else if eq $tlsMode "certmanager" -}}
{{- if $tlsRequested -}}
{{- if not (.Capabilities.APIVersions.Has "cert-manager.io/v1") -}}
{{- fail "tls.mode=certManager requires the cert-manager.io/v1 API to be available during render/install" -}}
{{- end -}}
{{- if not (trim .Values.certManager.issuerRef.kind) -}}
{{- fail "certManager.issuerRef.kind is required when tls.mode=certManager" -}}
{{- end -}}
{{- if not (trim .Values.certManager.issuerRef.name) -}}
{{- fail "certManager.issuerRef.name is required when tls.mode=certManager" -}}
{{- end -}}
{{- $cn := trim .Values.certManager.commonName -}}
{{- $hasDNS := gt (len .Values.certManager.dnsNames) 0 -}}
{{- $hasAlt := gt (len .Values.certManager.altNames) 0 -}}
{{- if and (not $cn) (not $hasDNS) (not $hasAlt) -}}
{{- fail "certManager.commonName or certManager.dnsNames/altNames must provide at least one DNS name when tls.mode=certManager" -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if and (eq $mode "acs") .Values.secrets.create -}}
{{- if not .Values.secrets.acsConnectionString -}}
{{- fail "secrets.acsConnectionString is required when deliveryMode=acs and secrets.create=true" -}}
{{- end -}}
{{- if not .Values.secrets.acsSender -}}
{{- fail "secrets.acsSender is required when deliveryMode=acs and secrets.create=true" -}}
{{- end -}}
{{- end -}}
{{- if eq $mode "ses" -}}
{{- if not .Values.ses.region -}}
{{- fail "ses.region is required when deliveryMode=ses" -}}
{{- end -}}
{{- if not .Values.ses.sender -}}
{{- fail "ses.sender is required when deliveryMode=ses" -}}
{{- end -}}
{{- $access := default "" .Values.secrets.sesAccessKeyID -}}
{{- $secret := default "" .Values.secrets.sesSecretAccessKey -}}
{{- if and (or (and $access (not $secret)) (and $secret (not $access))) .Values.secrets.create -}}
{{- fail "secrets.sesAccessKeyID and secrets.sesSecretAccessKey must both be set or both be empty when secrets.create=true" -}}
{{- end -}}
{{- if and .Values.secrets.create .Values.secrets.sesSessionToken (or (not $access) (not $secret)) -}}
{{- fail "secrets.sesSessionToken requires both secrets.sesAccessKeyID and secrets.sesSecretAccessKey when secrets.create=true" -}}
{{- end -}}
{{- end -}}
{{- if and (has $mode (list "acs" "ses")) (gt (int .Values.replicaCount) 1) -}}
{{- fail "replicaCount > 1 is not supported for deliveryMode=acs or ses because SQLite spool storage requires a single writer on block-backed ReadWriteOnce storage" -}}
{{- end -}}
{{- if and (has $mode (list "acs" "ses")) (not .Values.spool.persistence.enabled) -}}
{{- fail "spool.persistence.enabled must be true for deliveryMode=acs or ses because durable relay semantics require persistent single-writer block storage" -}}
{{- end -}}
{{- if .Values.extraTrustedCA.enabled -}}
{{- if not (trim .Values.extraTrustedCA.existingSecret) -}}
{{- fail "extraTrustedCA.existingSecret is required when extraTrustedCA.enabled=true" -}}
{{- end -}}
{{- if not (trim .Values.extraTrustedCA.key) -}}
{{- fail "extraTrustedCA.key is required when extraTrustedCA.enabled=true" -}}
{{- end -}}
{{- end -}}
{{- if and .Values.networkPolicy.enabled .Values.networkPolicy.egressCIDRs (not $egressPorts) -}}
{{- fail "networkPolicy.egressTCPPorts must contain at least one entry when networkPolicy.egressCIDRs is configured" -}}
{{- end -}}
{{- range $i, $port := $egressPorts -}}
{{- if or (lt (int $port) 1) (gt (int $port) 65535) -}}
{{- fail (printf "networkPolicy.egressTCPPorts[%d] must be between 1 and 65535" $i) -}}
{{- end -}}
{{- end -}}
{{- if .Values.networkPolicy.enabled -}}
{{- include "smtp-cloud-relay.validateURLPortAllowed" (dict "label" "proxy.httpProxy" "url" .Values.proxy.httpProxy "allowedPorts" $egressPorts) -}}
{{- include "smtp-cloud-relay.validateURLPortAllowed" (dict "label" "proxy.httpsProxy" "url" .Values.proxy.httpsProxy "allowedPorts" $egressPorts) -}}
{{- include "smtp-cloud-relay.validateURLPortAllowed" (dict "label" "acs.endpoint" "url" .Values.acs.endpoint "allowedPorts" $egressPorts) -}}
{{- include "smtp-cloud-relay.validateURLPortAllowed" (dict "label" "ses.endpoint" "url" .Values.ses.endpoint "allowedPorts" $egressPorts) -}}
{{- end -}}
{{- end -}}

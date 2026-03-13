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

{{- define "smtp-cloud-relay.validateValues" -}}
{{- $mode := lower (default "acs" .Values.deliveryMode) -}}
{{- if not (has $mode (list "acs" "ses" "noop")) -}}
{{- fail (printf "deliveryMode must be one of: acs, ses, noop (got %q)" .Values.deliveryMode) -}}
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
{{- end -}}

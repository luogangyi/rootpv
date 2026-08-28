{{- define "rootpv.appName" -}}
{{- .Values.names.app | default "persistent-rootfs" -}}
{{- end -}}

{{- define "rootpv.controllerName" -}}
{{- .Values.names.controller | default "persistent-rootfs-controller" -}}
{{- end -}}

{{- define "rootpv.nodeAgentName" -}}
{{- .Values.names.nodeAgent | default "persistent-rootfsd" -}}
{{- end -}}

{{- define "rootpv.webhookServiceName" -}}
{{- .Values.names.webhookService | default "persistent-rootfs-webhook" -}}
{{- end -}}

{{- define "rootpv.webhookTLSSecretName" -}}
{{- default (.Values.names.webhookTLSSecret | default "persistent-rootfs-webhook-tls") .Values.webhook.certs.existingSecret -}}
{{- end -}}

{{- define "rootpv.configMapName" -}}
{{- .Values.names.configMap | default "persistent-rootfs-webhook-config" -}}
{{- end -}}

{{- define "rootpv.controllerServiceAccountName" -}}
{{- .Values.names.controllerServiceAccount | default "persistent-rootfs-controller" -}}
{{- end -}}

{{- define "rootpv.nodeServiceAccountName" -}}
{{- .Values.names.nodeServiceAccount | default "persistent-rootfs-node" -}}
{{- end -}}

{{- define "rootpv.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" -}}
{{- end -}}

{{- define "rootpv.labels" -}}
helm.sh/chart: {{ include "rootpv.chart" . }}
app.kubernetes.io/name: {{ include "rootpv.appName" . }}
app.kubernetes.io/part-of: rootpv
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "rootpv.controllerLabels" -}}
{{ include "rootpv.labels" . }}
app.kubernetes.io/component: controller
{{- end -}}

{{- define "rootpv.nodeAgentLabels" -}}
{{ include "rootpv.labels" . }}
app.kubernetes.io/component: node-agent
{{- end -}}

{{- define "rootpv.selectorLabels" -}}
app.kubernetes.io/name: {{ include "rootpv.appName" . }}
{{- end -}}

{{- define "rootpv.controllerSelectorLabels" -}}
{{ include "rootpv.selectorLabels" . }}
app.kubernetes.io/component: controller
{{- end -}}

{{- define "rootpv.nodeAgentSelectorLabels" -}}
{{ include "rootpv.selectorLabels" . }}
app.kubernetes.io/component: node-agent
{{- end -}}

{{- define "rootpv.image" -}}
{{- printf "%s:%s" .Values.image.repository (.Values.image.tag | default .Chart.AppVersion) -}}
{{- end -}}

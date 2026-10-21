import { Button, Checkbox, Form, Input, InputNumber, Select } from 'antd'
import { useTranslation } from '../i18n/index.ts'
import { buildElicitationContent } from '../elicitation.ts'
import type { ElicitationAction, ElicitationField, ElicitationRequest } from '../elicitation.ts'
import styles from './ElicitationForm.module.css'

/**
 * ElicitationForm renders an ACP form-mode elicitation request. Values are
 * validated against the offered schema before submit; a validation failure
 * stays local and never reaches the adapter (which answers `cancel` for
 * invalid accept payloads). Decline and Cancel are always available.
 */
export function ElicitationForm({
  request,
  onSubmit,
}: {
  request: ElicitationRequest
  onSubmit: (action: ElicitationAction, content?: Record<string, unknown>) => void
}) {
  const { t } = useTranslation('session')
  const [form] = Form.useForm()
  const initialValues: Record<string, unknown> = {}
  for (const field of request.fields) {
    if (field.default !== undefined) initialValues[field.name] = field.default
    else if (field.kind === 'boolean') initialValues[field.name] = false
  }
  const hasUnsupported = request.fields.some((field) => field.kind === 'unsupported')

  const accept = () => {
    const values = form.getFieldsValue(true) as Record<string, unknown>
    const { content, errors } = buildElicitationContent(request, values)
    const failed = Object.entries(errors)
    if (failed.length > 0) {
      form.setFields(
        failed.map(([name, code]) => ({
          name,
          errors: [t(`elicitation.errors.${code}`)],
        }))
      )
      return
    }
    onSubmit('accept', content)
  }

  return (
    <section className={styles.card} role="group" aria-label={t('elicitation.required')}>
      <p className={styles.heading}>{t('elicitation.required')}</p>
      {request.message && <p className={styles.question}>{request.message}</p>}
      <Form
        form={form}
        layout="vertical"
        initialValues={initialValues}
        onValuesChange={(changed: Record<string, unknown>) => {
          for (const name of Object.keys(changed)) {
            form.setFields([{ name, errors: [] }])
          }
        }}
      >
        {request.fields.map((field) => (
          <Form.Item
            key={field.name}
            name={field.name}
            label={field.title || t('elicitation.answer')}
            extra={field.description}
            required={field.required}
            valuePropName={field.kind === 'boolean' ? 'checked' : undefined}
            className={styles.field}
          >
            <FieldControl field={field} />
          </Form.Item>
        ))}
      </Form>
      <div className={styles.actions}>
        <Button type="primary" onClick={accept} disabled={hasUnsupported}>
          {t('elicitation.accept')}
        </Button>
        <Button danger onClick={() => onSubmit('decline')}>
          {t('elicitation.decline')}
        </Button>
        <Button onClick={() => onSubmit('cancel')}>{t('elicitation.cancel')}</Button>
      </div>
      {hasUnsupported && (
        <p className={styles.unsupported} role="alert">
          {t('elicitation.unsupported')}
        </p>
      )}
    </section>
  )
}

// FieldControl must forward the value/onChange props Form.Item injects into
// its direct child; wrapping the control in another component that drops them
// leaves the form store empty even though the DOM shows typed text.
function FieldControl({
  field,
  ...control
}: { field: ElicitationField } & Record<string, unknown>) {
  const { t } = useTranslation('session')
  switch (field.kind) {
    case 'select':
      return (
        <Select
          {...control}
          options={field.options?.map((option) => ({ value: option.value, label: option.label }))}
          allowClear={!field.required}
          showSearch
          optionFilterProp="label"
        />
      )
    case 'multiselect':
      return (
        <Select
          {...control}
          mode="multiple"
          options={field.options?.map((option) => ({ value: option.value, label: option.label }))}
          allowClear={!field.required}
          optionFilterProp="label"
        />
      )
    case 'number':
    case 'integer':
      return (
        <InputNumber
          {...control}
          min={field.minimum}
          max={field.maximum}
          precision={field.kind === 'integer' ? 0 : undefined}
          style={{ width: '100%' }}
        />
      )
    case 'boolean':
      return <Checkbox {...control} />
    case 'unsupported':
      return <p className={styles.unsupported}>{t('elicitation.unsupported')}</p>
    case 'string':
    default: {
      const type =
        field.format === 'email'
          ? 'email'
          : field.format === 'uri'
            ? 'url'
            : field.format === 'date'
              ? 'date'
              : field.format === 'date-time'
                ? 'datetime-local'
                : 'text'
      return <Input {...control} type={type} />
    }
  }
}

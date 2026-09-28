import { Button, Dropdown } from 'antd'
import { useTranslation, setLanguage, type Lang } from '../i18n/index.ts'

function GlobeIcon() {
  return (
    <svg viewBox="0 0 24 24" width="16" height="16" aria-hidden="true" focusable="false">
      <circle cx="12" cy="12" r="8.5" fill="none" stroke="currentColor" strokeWidth="1.5" />
      <path
        d="M3.5 12h17M12 3.5c2.5 2.4 2.5 14.6 0 17M12 3.5c-2.5 2.4-2.5 14.6 0 17"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.2"
      />
    </svg>
  )
}

/**
 * Language picker for the header: English / 中文, persisted via setLanguage.
 * Labels stay in their own language (a language name is never translated).
 */
export function LanguageSwitcher() {
  const { t, i18n } = useTranslation('nav')
  const current = (i18n.resolvedLanguage ?? i18n.language).startsWith('zh') ? 'zh-CN' : 'en'
  return (
    <Dropdown
      trigger={['click']}
      placement="bottomRight"
      menu={{
        selectable: true,
        selectedKeys: [current],
        items: [
          { key: 'en', label: t('languageEnglish') },
          { key: 'zh-CN', label: t('languageChinese') },
        ],
        onClick: ({ key }) => setLanguage(key as Lang),
      }}
    >
      <Button type="text" aria-label={t('language')} title={t('language')}>
        <GlobeIcon />
      </Button>
    </Dropdown>
  )
}

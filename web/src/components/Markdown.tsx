import ReactMarkdown, { type Components } from 'react-markdown'
import remarkBreaks from 'remark-breaks'
import remarkGfm from 'remark-gfm'
import styles from './Markdown.module.css'

// GFM adds tables/task lists/strikethrough; breaks keeps a plain-text line
// break a line break, so a message that was never markdown reads unchanged.
const remarkPlugins = [remarkGfm, remarkBreaks]

// react-markdown ignores raw HTML unless rehype-raw is added: agent output
// cannot inject markup. Links leave the app without handing over window.opener.
const components: Components = {
  a({ href, title, children }) {
    return (
      <a href={href} title={title} target="_blank" rel="noopener noreferrer">
        {children}
      </a>
    )
  },
  table({ children }) {
    return (
      <div className={styles.tableWrap}>
        <table>{children}</table>
      </div>
    )
  },
}

interface MarkdownProps {
  content: string
  className?: string
  /** Render the typing caret after the last block (live streamed turns). */
  streaming?: boolean
}

export function Markdown({ content, className, streaming }: MarkdownProps) {
  return (
    <div
      className={className ? `${styles.markdown} ${className}` : styles.markdown}
      data-streaming={streaming ? 'true' : undefined}
    >
      <ReactMarkdown remarkPlugins={remarkPlugins} components={components}>
        {content}
      </ReactMarkdown>
    </div>
  )
}

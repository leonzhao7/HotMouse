import type { SVGProps } from 'react'

/** GitHub 风格的线性图标 (24x24 stroke 图标，视觉上贴近 Octicon)。 */

type IconProps = SVGProps<SVGSVGElement>

function Icon({ children, ...props }: IconProps) {
  return (
    <svg
      aria-hidden="true"
      fill="none"
      height="16"
      viewBox="0 0 16 16"
      width="16"
      xmlns="http://www.w3.org/2000/svg"
      {...props}
    >
      {children}
    </svg>
  )
}

export function IconRepo(props: IconProps) {
  return (
    <Icon {...props}>
      <path
        d="M2 2.5A2.5 2.5 0 0 1 4.5 0h8.75a.75.75 0 0 1 .75.75v12.5a.75.75 0 0 1-.75.75h-2.5a.75.75 0 0 1 0-1.5h1.75v-2h-8a1 1 0 0 0-.714 1.7.75.75 0 1 1-1.072 1.05A2.495 2.495 0 0 1 2 11.5Zm10.5-1h-8a1 1 0 0 0-1 1v6.708A2.486 2.486 0 0 1 4.5 9h8ZM5 12.25a.25.25 0 0 1 .25-.25h3.5a.25.25 0 0 1 .25.25v3.25a.25.25 0 0 1-.4.2l-1.45-1.087a.249.249 0 0 0-.3 0L5.4 15.7a.25.25 0 0 1-.4-.2Z"
        fill="currentColor"
      />
    </Icon>
  )
}

export function IconPlus(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M7.75 2a.75.75 0 0 1 .75.75V7h4.25a.75.75 0 0 1 0 1.5H8.5v4.25a.75.75 0 0 1-1.5 0V8.5H2.75a.75.75 0 0 1 0-1.5H7V2.75A.75.75 0 0 1 7.75 2Z" fill="currentColor" />
    </Icon>
  )
}

export function IconCheck(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M13.78 4.22a.75.75 0 0 1 0 1.06l-7.25 7.25a.75.75 0 0 1-1.06 0L2.22 9.28a.75.75 0 0 1 1.06-1.06L6 10.94l6.72-6.72a.75.75 0 0 1 1.06 0Z" fill="currentColor" />
    </Icon>
  )
}

export function IconTarget(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M8 0a8 8 0 1 1 0 16A8 8 0 0 1 8 0Zm0 1.5a6.5 6.5 0 1 0 0 13 6.5 6.5 0 0 0 0-13Zm0 3a3.5 3.5 0 1 1 0 7 3.5 3.5 0 0 1 0-7Z" fill="currentColor" />
    </Icon>
  )
}

export function IconLink(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="m7.775 3.275 1.25-1.25a3.5 3.5 0 1 1 4.95 4.95l-2.5 2.5a3.5 3.5 0 0 1-4.95 0 .75.75 0 0 1 1.06-1.06 2 2 0 0 0 2.83 0l2.5-2.5a2 2 0 0 0-2.83-2.83l-1.25 1.25a.75.75 0 0 1-1.06-1.06ZM8.225 12.725l-1.25 1.25a2 2 0 0 1-2.83-2.83l2.5-2.5a2 2 0 0 1 2.83 0 .75.75 0 0 1-1.06 1.06 2 2 0 0 0 0 2.83l1.25-1.25a.75.75 0 0 1 1.06 1.06Z" fill="currentColor" />
    </Icon>
  )
}

export function IconArrowRight(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M8.22 2.22a.75.75 0 0 1 1.06 0l4.25 4.25a.75.75 0 0 1 0 1.06l-4.25 4.25a.75.75 0 1 1-1.06-1.06L11.44 8H2.75a.75.75 0 0 1 0-1.5h8.69L8.22 3.28a.75.75 0 0 1 0-1.06Z" fill="currentColor" />
    </Icon>
  )
}

export function IconAlert(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M6.457 1.047c.659-1.234 2.427-1.234 3.086 0l6.082 11.378A1.75 1.75 0 0 1 14.082 15H1.918a1.75 1.75 0 0 1-1.543-2.575Zm1.763.707a.25.25 0 0 0-.44 0L1.698 13.132a.25.25 0 0 0 .22.368h12.164a.25.25 0 0 0 .22-.368Zm.53 3.996v2.5a.75.75 0 0 1-1.5 0v-2.5a.75.75 0 0 1 1.5 0ZM9 11a1 1 0 1 1-2 0 1 1 0 0 1 2 0Z" fill="currentColor" />
    </Icon>
  )
}

export function IconCheckCircle(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M8 0a8 8 0 1 1 0 16A8 8 0 0 1 8 0Zm3.53 4.97a.75.75 0 0 1 0 1.06L6.75 10.81l-2.28-2.28a.75.75 0 1 0-1.06 1.06l2.72 2.72a.75.75 0 0 0 1.06 0l5.34-5.34a.75.75 0 0 0 0-1.06Z" fill="currentColor" />
    </Icon>
  )
}

export function IconInfo(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M0 8a8 8 0 1 1 16 0A8 8 0 0 1 0 8Zm8-6.5a6.5 6.5 0 1 0 0 13 6.5 6.5 0 0 0 0-13ZM6.5 7.75A.75.75 0 0 1 7.25 7h1a.75.75 0 0 1 .75.75v2.75h.25a.75.75 0 0 1 0 1.5h-2a.75.75 0 0 1 0-1.5h.25v-2h-1a.75.75 0 0 1-.75-.75ZM8 6a1 1 0 1 1 0-2 1 1 0 0 1 0 2Z" fill="currentColor" />
    </Icon>
  )
}

export function IconSearch(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M10.68 11.74a6 6 0 0 1-7.922-7.922 6 6 0 0 1 7.922 7.922Zm-1.06-1.06a4.5 4.5 0 1 0-5.94 5.94 4.5 4.5 0 0 0 5.94-5.94Zm.529 5.122 3.585 3.585a.75.75 0 1 1-1.06 1.06l-3.585-3.585a.75.75 0 0 1 1.06-1.06Z" fill="currentColor" />
    </Icon>
  )
}

export function IconLock(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M4 4a4 4 0 0 1 8 0v2h.25c.966 0 1.75.784 1.75 1.75v5.5A1.75 1.75 0 0 1 12.25 15h-8.5A1.75 1.75 0 0 1 2 13.25v-5.5C2 6.784 2.784 6 3.75 6H4Zm1.5 2h5V4a2.5 2.5 0 0 0-5 0Zm-.25 1.5a.25.25 0 0 0-.25.25v5.5c0 .138.112.25.25.25h8.5a.25.25 0 0 0 .25-.25v-5.5a.25.25 0 0 0-.25-.25Z" fill="currentColor" />
    </Icon>
  )
}

export function IconEye(props: IconProps) {
  return (
    <Icon {...props}>
      <path
        d="M8 2C4.75 2 1.9 4.32.5 8c1.4 3.68 4.25 6 7.5 6s6.1-2.32 7.5-6C14.1 4.32 11.25 2 8 2Zm0 10.5A5.9 5.9 0 0 1 2.15 8 5.9 5.9 0 0 1 8 3.5 5.9 5.9 0 0 1 13.85 8 5.9 5.9 0 0 1 8 12.5Zm0-2A2.5 2.5 0 1 0 8 5a2.5 2.5 0 0 0 0 5.5Z"
        fill="currentColor"
      />
    </Icon>
  )
}

export function IconEyeClosed(props: IconProps) {
  return (
    <Icon {...props}>
      <path
        d="M8 2C4.75 2 1.9 4.32.5 8c1.4 3.68 4.25 6 7.5 6s6.1-2.32 7.5-6C14.1 4.32 11.25 2 8 2Zm0 10.5A5.9 5.9 0 0 1 2.15 8 5.9 5.9 0 0 1 8 3.5 5.9 5.9 0 0 1 13.85 8 5.9 5.9 0 0 1 8 12.5Zm0-2A2.5 2.5 0 1 0 8 5a2.5 2.5 0 0 0 0 5.5Z"
        fill="currentColor"
      />
      <path
        d="m2 2 12 12"
        fill="none"
        stroke="currentColor"
        strokeLinecap="round"
        strokeWidth="1.5"
      />
    </Icon>
  )
}

export function IconTrash(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M11 1.75V3h2.25a.75.75 0 0 1 0 1.5H2.75a.75.75 0 0 1 0-1.5H5V1.75C5 .784 5.784 0 6.75 0h2.5C10.216 0 11 .784 11 1.75ZM4.496 6.675l.66 6.6a.25.25 0 0 0 .249.225h5.19a.25.25 0 0 0 .249-.225l.66-6.6a.75.75 0 0 1 1.492.149l-.66 6.6A1.75 1.75 0 0 1 10.595 15h-5.19a1.75 1.75 0 0 1-1.741-1.575l-.66-6.6a.75.75 0 1 1 1.492-.15ZM6.5 1.75V3h3V1.75a.25.25 0 0 0-.25-.25h-2.5a.25.25 0 0 0-.25.25Z" fill="currentColor" />
    </Icon>
  )
}

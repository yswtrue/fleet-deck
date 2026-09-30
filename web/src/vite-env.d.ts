/// <reference types="vite/client" />

// Query-directive imports for vite-imagetools; each used width/format pair
// needs an exact literal-suffix declaration (one `*` per pattern only).
declare module '*.png?w=64&format=webp' {
  const src: string
  export default src
}

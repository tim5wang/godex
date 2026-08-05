declare module "plantuml-encoder" {
  /** Encode a PlantUML source document (e.g. "@startuml … @enduml") for use in a PlantUML server URL. */
  export function encode(text: string): string;
  /** Decode an encoded PlantUML payload back to source text. */
  export function decode(encoded: string): string;
}

// @flow
import * as React from 'react'
import {Box, Text} from '.'
import type {Props} from './form-input'
import {NativeTextInput} from './native-wrappers.native'
import {globalColors, globalMargins, globalStyles} from '../styles'
import {getStyle} from './text'

type State = {
  value: string,
  focused: boolean,
}

export const backgroundModeToColor = {
  Normal: globalColors.white,
  Terminal: globalColors.darkBlue3,
  Announcements: globalColors.blue,
  Success: globalColors.green,
  Information: globalColors.yellow,
  HighRisk: globalColors.red,
  Documentation: globalColors.darkBlue,
}

class FormInput extends React.Component<Props, State> {
  state: State
  _input: any

  constructor(props: Props) {
    super(props)

    this.state = {
      value: this.props.value || '',
      focused: false,
    }
  }

  getValue = (): string => {
    return this.state.value || ''
  }

  setValue = (value: string) => {
    this.setState({value: value || ''})
  }

  clearValue = () => {
    this._onChangeText('')
  }

  focus = () => {
    this._input && this._input.focus()
  }

  blur = () => {
    this._input && this._input.blur()
  }

  select = () => {
    this._input && this._input.select()
  }

  _onChangeText = (text: string) => {
    this.setState({value: text || ''})

    this.props.onChangeText && this.props.onChangeText(text || '')
  }

  _onFocus = () => {
    this.setState({
      focused: true,
    })
  }

  _onBlur = () => {
    this.setState({
      focused: false,
    })
  }

  render() {
    let backgroundColor = this.props.backgroundMode
      ? backgroundModeToColor[this.props.backgroundMode]
      : globalColors.white
    return (
      <Box
        style={{
          ...globalStyles.flexBoxColumn,
          alignItems: 'center',
          justifyContent: 'center',
          position: 'relative',
          width: '100%',
          borderTopWidth: 1,
          borderBottomWidth: this.props.hideBottomBorder ? 0 : 1,
          borderColor: globalColors.black_10,
          height: this.props.multiline ? 128 : 64,
          ...(this.props.maxHeight ? {maxHeight: this.props.maxHeight} : {}),
          ...this.props.containerStyle,
        }}
      >
        {(!!this.state.value || this.state.focused) && (
          <Text
            type="BodySmallSemibold"
            style={{
              position: 'absolute',
              top: globalMargins.tiny,
              left: globalMargins.small,
              color: globalColors.blue,
              fontSize: 11,
              zIndex: 2,
              backgroundColor: globalColors.transparent,
            }}
          >
            {this.props.label}
          </Text>
        )}
        {this.props.label &&
          this.props.multiline && (
            <Box
              style={{
                position: 'absolute',
                top: 0,
                left: 0,
                right: 0,
                height: 22,
                backgroundColor: backgroundColor,
                zIndex: 1,
              }}
            />
          )}
        <NativeTextInput
          value={this.state.value}
          onChangeText={this._onChangeText}
          placeholder={this.state.focused ? '' : this.props.label}
          ref={input => (this._input = input)}
          onFocus={this._onFocus}
          onBlur={this._onBlur}
          secureTextEntry={this.props.secure}
          multiline={this.props.multiline}
          style={{
            flex: 1,
            width: '100%',
            height: '100%',
            paddingLeft: globalMargins.small,
            paddingRight: globalMargins.small,
            paddingTop: globalMargins.medium - (this.props.multiline && !this.state.focused ? 3 : 0), // for some reason multiline inserts a tiny top padding
            paddingBottom: globalMargins.medium,
            lineHeight: 20,
            ...((this.props.textType && getStyle(this.props.textType, this.props.backgroundMode)) ||
              getStyle('BodySemibold')),
            ...this.props.inputStyle,
          }}
        />
      </Box>
    )
  }
}

export default FormInput